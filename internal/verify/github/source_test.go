package github_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AndrewMaged814/safelane/internal/verify/github"
)

// fixtureAPI is a GitHub-shaped server: a map from path to response body.
// Anything not in the map is a 404, which is what GitHub says too.
func fixtureAPI(t *testing.T, routes map[string]string) *github.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path
		if r.URL.RawQuery != "" {
			if body, ok := routes[key+"?"+r.URL.RawQuery]; ok {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
				return
			}
		}
		body, ok := routes[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return &github.Client{BaseURL: server.URL}
}

func repoRoutes() map[string]string {
	return map[string]string{
		"/repos/acme/payments-api": `{"full_name":"acme/payments-api","default_branch":"main"}`,
		"/repos/acme/payments-api/branches/main": `{
            "name":"main","protected":true,
            "commit":{"sha":"` + headSHA + `"},
            "protection":{"required_status_checks":{"contexts":["build-and-push"],"checks":[{"context":"test"}]}}
        }`,
	}
}

func TestRepositoryReadsProtectionAndRequiredChecks(t *testing.T) {
	client := fixtureAPI(t, repoRoutes())

	repo, err := client.Repository(context.Background(), "acme", "payments-api")
	if err != nil {
		t.Fatalf("Repository: %v", err)
	}
	if repo.DefaultBranch != "main" || !repo.Protected {
		t.Errorf("repository = %+v", repo)
	}
	// Both shapes GitHub reports required checks in, merged without
	// duplicates.
	if len(repo.RequiredChecks) != 2 {
		t.Errorf("required checks = %v", repo.RequiredChecks)
	}
}

// A branch with no protection reports no protection, rather than an error.
func TestRepositoryReportsMissingBranchProtection(t *testing.T) {
	routes := repoRoutes()
	routes["/repos/acme/payments-api/branches/main"] = `{"name":"main","protected":false,"commit":{"sha":"` + headSHA + `"}}`
	client := fixtureAPI(t, routes)

	repo, err := client.Repository(context.Background(), "acme", "payments-api")
	if err != nil {
		t.Fatalf("Repository: %v", err)
	}
	if repo.Protected || len(repo.RequiredChecks) != 0 {
		t.Errorf("repository = %+v", repo)
	}
}

func TestDefaultHeadReadsTheBranchTip(t *testing.T) {
	routes := repoRoutes()
	routes["/repos/acme/payments-api/commits/main"] = `{
        "sha":"` + headSHA + `",
        "commit":{"message":"feat: add refunds\n\nlonger body","author":{"name":"Andrew","date":"2026-08-20T10:00:00Z"},
                  "committer":{"date":"2026-08-20T10:01:00Z"}}
    }`
	client := fixtureAPI(t, routes)

	head, err := client.DefaultHead(context.Background(), "acme/payments-api")
	if err != nil {
		t.Fatalf("DefaultHead: %v", err)
	}
	if head.SHA != headSHA {
		t.Errorf("sha = %s", head.SHA)
	}
	// The subject is the first line. The body is not a subject.
	if head.Subject != "feat: add refunds" {
		t.Errorf("subject = %q", head.Subject)
	}
	if !head.OnDefaultBranch {
		t.Error("the default branch head is on the default branch")
	}
}

// The comparison carries every commit in the range, not the last merge. A
// release that skipped three merges has three merges' worth of change in it.
func TestCompareCarriesEveryCommitInTheRange(t *testing.T) {
	routes := repoRoutes()
	routes["/repos/acme/payments-api/compare/"+deployedSHA+"..."+headSHA] = `{
        "status":"ahead","ahead_by":3,"behind_by":0,
        "commits":[
          {"sha":"` + olderSHA + `","commit":{"message":"chore: bump deps","author":{"name":"A","date":"2026-08-18T09:00:00Z"}}},
          {"sha":"` + forkSHA + `","commit":{"message":"Merge pull request #61 from acme/refunds\n\nfeat: refunds","author":{"name":"B","date":"2026-08-19T09:00:00Z"}}},
          {"sha":"` + headSHA + `","commit":{"message":"feat: add refunds (#62)","author":{"name":"C","date":"2026-08-20T09:00:00Z"}}}
        ],
        "files":[{"filename":"internal/refunds.go","status":"added","additions":64,"deletions":12}]
    }`
	client := fixtureAPI(t, routes)

	comparison, err := client.Compare(context.Background(), "acme/payments-api", deployedSHA, headSHA)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if comparison.Status != "ahead" || comparison.AheadBy != 3 {
		t.Errorf("comparison = %+v", comparison)
	}
	if len(comparison.Commits) != 3 {
		t.Fatalf("commits = %d, want 3", len(comparison.Commits))
	}
	if len(comparison.Files) != 1 || comparison.Files[0].Additions != 64 {
		t.Errorf("files = %+v", comparison.Files)
	}

	// Pull requests appear only as provenance summaries: which pull requests
	// this range came through. The commits are the evidence.
	if len(comparison.PullRequests) != 2 {
		t.Fatalf("pull requests = %+v", comparison.PullRequests)
	}
	found := map[int]github.PullRequestSummary{}
	for _, pr := range comparison.PullRequests {
		found[pr.Number] = pr
	}
	// A merge commit's subject names the branch it came from; the pull
	// request's title lives in the body, which a provenance summary does not
	// need.
	if got := found[61]; got.Summary != "acme/refunds" || got.Merge != forkSHA {
		t.Errorf("merge-commit PR = %+v", got)
	}
	// A squash merge puts the title in the subject.
	if got := found[62]; got.Title != "feat: add refunds" {
		t.Errorf("squash-merge PR = %+v", got)
	}
}

// A check run reported against another commit is somebody else's evidence.
func TestChecksIgnoreOtherRevisions(t *testing.T) {
	routes := repoRoutes()
	routes["/repos/acme/payments-api/commits/"+headSHA+"/check-runs?per_page=100"] = `{
        "check_runs":[
          {"id":1,"name":"build-and-push","status":"completed","conclusion":"success","head_sha":"` + headSHA + `"},
          {"id":2,"name":"build-and-push","status":"completed","conclusion":"failure","head_sha":"` + olderSHA + `"}
        ]
    }`
	routes["/repos/acme/payments-api/actions/runs?head_sha="+headSHA+"&per_page=100"] = `{
        "workflow_runs":[
          {"id":41,"name":"build-and-push","status":"completed","conclusion":"success","head_sha":"` + headSHA + `"},
          {"id":42,"name":"build-and-push","status":"completed","conclusion":"success","head_sha":"` + olderSHA + `"}
        ]
    }`
	client := fixtureAPI(t, routes)

	checks, err := client.Checks(context.Background(), "acme/payments-api", headSHA)
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	if len(checks.Runs) != 1 || checks.Runs[0].Conclusion != "success" {
		t.Errorf("runs = %+v", checks.Runs)
	}
	if len(checks.Workflows) != 1 || checks.Workflows[0].ID != 41 {
		t.Errorf("workflows = %+v", checks.Workflows)
	}
	if got := checks.SuccessfulWorkflows(); len(got) != 1 {
		t.Errorf("successful workflows = %+v", got)
	}
}

// A commit the default branch is not at or ahead of is not on the default
// branch.
func TestRevisionReportsDefaultBranchMembership(t *testing.T) {
	routes := repoRoutes()
	routes["/repos/acme/payments-api/commits/"+forkSHA] = `{
        "sha":"` + forkSHA + `","commit":{"message":"wip","author":{"name":"A","date":"2026-08-20T09:00:00Z"}}}`
	routes["/repos/acme/payments-api/compare/"+forkSHA+"...main"] = `{"status":"diverged","ahead_by":1,"behind_by":2}`
	routes["/repos/acme/payments-api/commits/"+olderSHA] = `{
        "sha":"` + olderSHA + `","commit":{"message":"chore: bump deps","author":{"name":"A","date":"2026-08-18T09:00:00Z"}}}`
	routes["/repos/acme/payments-api/compare/"+olderSHA+"...main"] = `{"status":"ahead","ahead_by":2,"behind_by":0}`
	client := fixtureAPI(t, routes)

	fork, err := client.Revision(context.Background(), "acme/payments-api", forkSHA)
	if err != nil {
		t.Fatalf("Revision: %v", err)
	}
	if fork.OnDefaultBranch {
		t.Error("a diverged commit was reported as on the default branch")
	}

	older, err := client.Revision(context.Background(), "acme/payments-api", olderSHA)
	if err != nil {
		t.Fatalf("Revision: %v", err)
	}
	if !older.OnDefaultBranch {
		t.Error("an ancestor of the default branch is on the default branch")
	}
}

func TestARepositoryThatIsNotOwnerSlashNameIsRefused(t *testing.T) {
	client := fixtureAPI(t, repoRoutes())
	if _, err := client.DefaultHead(context.Background(), "payments-api"); err == nil {
		t.Fatal("accepted a repository that is not owner/name")
	}
}
