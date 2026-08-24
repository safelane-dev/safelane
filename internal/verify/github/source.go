package github

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Source is everything SafeLane needs to read from the source host: what the
// repository is, what its default branch currently points at, whether a named
// revision is real and on that branch, everything that changed between two
// revisions, and what CI said about one.
//
// It is an interface so the whole candidate-and-eligibility path runs against
// fixtures, and so a GitHub Enterprise host is a different value rather than a
// different code path.
type Source interface {
	Repository(ctx context.Context, owner, name string) (Repository, error)
	DefaultHead(ctx context.Context, repository string) (Revision, error)
	Revision(ctx context.Context, repository, sha string) (Revision, error)
	Compare(ctx context.Context, repository, base, head string) (Comparison, error)
	Checks(ctx context.Context, repository, sha string) (Checks, error)
}

// Repository is the registered repository as GitHub describes it.
type Repository struct {
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	// Protected is whether the default branch has branch protection at all.
	Protected bool `json:"protected"`
	// RequiredChecks are the check names branch protection requires. An empty
	// list on a protected branch and an empty list on an unprotected one mean
	// different things, which is why Protected is separate.
	RequiredChecks []string `json:"required_checks,omitempty"`
}

// Revision is one commit. The SHA is always full: an abbreviated SHA is a
// prefix, and SafeLane does not deploy prefixes.
type Revision struct {
	SHA         string    `json:"sha"`
	Subject     string    `json:"subject"`
	Author      string    `json:"author,omitempty"`
	CommittedAt time.Time `json:"committed_at"`
	// OnDefaultBranch is whether this commit is in the default branch's
	// history. A commit that exists but sits on somebody's fork is a commit
	// that exists.
	OnDefaultBranch bool `json:"on_default_branch"`
}

// Comparison is the complete `base...head` range.
type Comparison struct {
	Base string `json:"base"`
	Head string `json:"head"`
	// Status is GitHub's own word: identical, ahead, behind, or diverged.
	Status   string `json:"status"`
	AheadBy  int    `json:"ahead_by"`
	BehindBy int    `json:"behind_by"`
	// Commits is every commit in the range, not the last merge. The
	// assessment is about what accumulated, and a release that skipped three
	// merges has three merges' worth of change in it.
	Commits []Revision `json:"commits"`
	// Files is every path the range touched.
	Files []FileChange `json:"files,omitempty"`
	// PullRequests are provenance summaries only - which pull requests this
	// range came through. They are not evidence about the change itself;
	// the commits are.
	PullRequests []PullRequestSummary `json:"pull_requests,omitempty"`
	// Diff is the immutable raw diff for base and head. It is used only to
	// create a content-addressed evidence handle and is not serialized into
	// the frozen release summary.
	Diff []byte `json:"-"`
}

// FileChange is one path in a comparison.
type FileChange struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// PullRequestSummary is a pull request named by a merge commit in the range.
type PullRequestSummary struct {
	Number  int    `json:"number"`
	Title   string `json:"title,omitempty"`
	Merge   string `json:"merge_commit"`
	Summary string `json:"summary,omitempty"`
}

// Checks is what CI reported for one exact revision.
type Checks struct {
	Revision string `json:"revision"`
	// Runs is every check run GitHub reported against this revision.
	Runs []CheckRun `json:"runs,omitempty"`
	// Workflows is every workflow run for this revision. The successful ones
	// are what can have produced the image.
	Workflows []WorkflowRun `json:"workflows,omitempty"`
}

// WorkflowRun is one GitHub Actions run against an exact revision.
type WorkflowRun struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	HeadSHA    string    `json:"head_sha"`
	URL        string    `json:"url,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

// Succeeded reports whether a run completed successfully.
func (w WorkflowRun) Succeeded() bool {
	return w.Status == "completed" && w.Conclusion == "success"
}

// SuccessfulWorkflows returns only the runs that could have produced an image.
func (c Checks) SuccessfulWorkflows() []WorkflowRun {
	var out []WorkflowRun
	for _, run := range c.Workflows {
		if run.Succeeded() {
			out = append(out, run)
		}
	}
	return out
}

// Run returns the named check run.
func (c Checks) Run(name string) (CheckRun, bool) {
	for _, run := range c.Runs {
		if run.Name == name {
			return run, true
		}
	}
	return CheckRun{}, false
}

// --- the real Source, over the GitHub REST API ---

type repositoryResponse struct {
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
}

type branchResponse struct {
	Name      string `json:"name"`
	Protected bool   `json:"protected"`
	Commit    struct {
		SHA string `json:"sha"`
	} `json:"commit"`
	Protection struct {
		RequiredStatusChecks struct {
			Contexts []string `json:"contexts"`
			Checks   []struct {
				Context string `json:"context"`
			} `json:"checks"`
		} `json:"required_status_checks"`
	} `json:"protection"`
}

type commitResponse struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
		Author  struct {
			Name string    `json:"name"`
			Date time.Time `json:"date"`
		} `json:"author"`
		Committer struct {
			Date time.Time `json:"date"`
		} `json:"committer"`
	} `json:"commit"`
}

func (c commitResponse) revision() Revision {
	committed := c.Commit.Committer.Date
	if committed.IsZero() {
		committed = c.Commit.Author.Date
	}
	return Revision{
		SHA:         c.SHA,
		Subject:     subjectOf(c.Commit.Message),
		Author:      c.Commit.Author.Name,
		CommittedAt: committed,
	}
}

type compareResponse struct {
	Status   string           `json:"status"`
	AheadBy  int              `json:"ahead_by"`
	BehindBy int              `json:"behind_by"`
	Commits  []commitResponse `json:"commits"`
	Files    []struct {
		Filename  string `json:"filename"`
		Status    string `json:"status"`
		Additions int    `json:"additions"`
		Deletions int    `json:"deletions"`
	} `json:"files"`
}

type workflowRunsResponse struct {
	WorkflowRuns []struct {
		ID         int64     `json:"id"`
		Name       string    `json:"name"`
		Status     string    `json:"status"`
		Conclusion string    `json:"conclusion"`
		HeadSHA    string    `json:"head_sha"`
		HTMLURL    string    `json:"html_url"`
		UpdatedAt  time.Time `json:"updated_at"`
	} `json:"workflow_runs"`
}

// Repository reads the repository and its default branch protection.
//
// Protection is read even when it is absent, because "this branch requires
// nothing" is a fact worth reporting rather than a reason to stay quiet.
func (c *Client) Repository(ctx context.Context, owner, name string) (Repository, error) {
	var repo repositoryResponse
	if err := c.do(ctx, "GET", fmt.Sprintf("/repos/%s/%s", owner, name), &repo); err != nil {
		return Repository{}, err
	}
	out := Repository{FullName: repo.FullName, DefaultBranch: repo.DefaultBranch}

	var branch branchResponse
	err := c.do(ctx, "GET", fmt.Sprintf("/repos/%s/%s/branches/%s", owner, name, url.PathEscape(repo.DefaultBranch)), &branch)
	if err != nil {
		// A branch SafeLane cannot read is not a branch with no protection.
		return out, err
	}
	out.Protected = branch.Protected
	out.RequiredChecks = append(out.RequiredChecks, branch.Protection.RequiredStatusChecks.Contexts...)
	for _, check := range branch.Protection.RequiredStatusChecks.Checks {
		if check.Context != "" && !contains(out.RequiredChecks, check.Context) {
			out.RequiredChecks = append(out.RequiredChecks, check.Context)
		}
	}
	return out, nil
}

// DefaultHead is the exact commit the default branch points at right now.
func (c *Client) DefaultHead(ctx context.Context, repository string) (Revision, error) {
	owner, name, err := splitRepository(repository)
	if err != nil {
		return Revision{}, err
	}
	repo, err := c.Repository(ctx, owner, name)
	if err != nil && repo.DefaultBranch == "" {
		return Revision{}, err
	}

	var commit commitResponse
	path := fmt.Sprintf("/repos/%s/%s/commits/%s", owner, name, url.PathEscape(repo.DefaultBranch))
	if err := c.do(ctx, "GET", path, &commit); err != nil {
		return Revision{}, err
	}
	revision := commit.revision()
	revision.OnDefaultBranch = true
	return revision, nil
}

// Revision reads one commit and reports whether it is on the default branch.
func (c *Client) Revision(ctx context.Context, repository, sha string) (Revision, error) {
	owner, name, err := splitRepository(repository)
	if err != nil {
		return Revision{}, err
	}
	var commit commitResponse
	if err := c.do(ctx, "GET", fmt.Sprintf("/repos/%s/%s/commits/%s", owner, name, url.PathEscape(sha)), &commit); err != nil {
		return Revision{}, err
	}
	revision := commit.revision()

	repo, repoErr := c.Repository(ctx, owner, name)
	if repoErr != nil && repo.DefaultBranch == "" {
		return revision, nil
	}
	// A commit is on the default branch when the branch is at it or ahead of
	// it. `behind` and `diverged` both mean the commit is somewhere else.
	comparison, cmpErr := c.Compare(ctx, repository, sha, repo.DefaultBranch)
	if cmpErr == nil {
		revision.OnDefaultBranch = comparison.Status == "identical" || comparison.Status == "ahead"
	}
	return revision, nil
}

// RevisionExists implements oci.RevisionChecker without making the OCI
// package depend on GitHub. A missing commit is a checked false; connectivity
// and authorization failures remain errors so confirmation never guesses.
func (c *Client) RevisionExists(ctx context.Context, repository, sha string) (bool, error) {
	owner, name, err := splitRepository(repository)
	if err != nil {
		return false, err
	}
	var commit commitResponse
	err = c.do(ctx, "GET", fmt.Sprintf("/repos/%s/%s/commits/%s", owner, name, url.PathEscape(sha)), &commit)
	if err == errNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return strings.EqualFold(commit.SHA, sha), nil
}

// Compare returns the complete base...head range.
func (c *Client) Compare(ctx context.Context, repository, base, head string) (Comparison, error) {
	owner, name, err := splitRepository(repository)
	if err != nil {
		return Comparison{}, err
	}
	path := fmt.Sprintf("/repos/%s/%s/compare/%s...%s", owner, name, url.PathEscape(base), url.PathEscape(head))
	var comparison Comparison
	comparison.Base, comparison.Head = base, head
	for page := 1; ; page++ {
		var response compareResponse
		pagePath := fmt.Sprintf("%s?per_page=100&page=%d", path, page)
		if err := c.do(ctx, "GET", pagePath, &response); err != nil {
			return Comparison{}, err
		}
		if page == 1 {
			comparison.Status = response.Status
			comparison.AheadBy = response.AheadBy
			comparison.BehindBy = response.BehindBy
			for _, file := range response.Files {
				comparison.Files = append(comparison.Files, FileChange{
					Path: file.Filename, Status: file.Status,
					Additions: file.Additions, Deletions: file.Deletions,
				})
			}
		}
		for _, commit := range response.Commits {
			comparison.Commits = append(comparison.Commits, commit.revision())
		}
		if len(comparison.Commits) >= comparison.AheadBy || len(response.Commits) == 0 {
			break
		}
	}
	if len(comparison.Commits) != comparison.AheadBy {
		return Comparison{}, fmt.Errorf("github comparison is incomplete: received %d of %d commits", len(comparison.Commits), comparison.AheadBy)
	}
	diff, err := c.read(ctx, path, "application/vnd.github.diff")
	if err != nil {
		return Comparison{}, err
	}
	comparison.Diff = diff
	for _, file := range filesInDiff(diff) {
		if !hasFile(comparison.Files, file) {
			comparison.Files = append(comparison.Files, FileChange{
				Path: file, Status: "changed",
			})
		}
	}
	comparison.PullRequests = pullRequestsIn(comparison.Commits)
	return comparison, nil
}

// RawDiff retrieves the exact source range behind a frozen diff handle. The
// caller verifies the returned bytes against that handle before exposing them.
func (c *Client) RawDiff(ctx context.Context, repository, base, head string) ([]byte, error) {
	owner, name, err := splitRepository(repository)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/repos/%s/%s/compare/%s...%s", owner, name, url.PathEscape(base), url.PathEscape(head))
	return c.read(ctx, path, "application/vnd.github.diff")
}

func hasFile(files []FileChange, path string) bool {
	for _, file := range files {
		if file.Path == path {
			return true
		}
	}
	return false
}

func filesInDiff(diff []byte) []string {
	var files []string
	for _, line := range strings.Split(string(diff), "\n") {
		if !strings.HasPrefix(line, "diff --git ") {
			continue
		}
		rest := strings.TrimPrefix(line, "diff --git ")
		marker := strings.LastIndex(rest, " b/")
		if marker < 0 {
			marker = strings.LastIndex(rest, " \"b/")
		}
		if marker < 0 {
			continue
		}
		path := strings.TrimSpace(rest[marker+1:])
		if strings.HasPrefix(path, "\"") {
			if decoded, err := strconv.Unquote(path); err == nil {
				path = decoded
			}
		}
		path = strings.TrimPrefix(path, "b/")
		if path != "" && !contains(files, path) {
			files = append(files, path)
		}
	}
	return files
}

// Checks reads what CI reported for one exact revision.
func (c *Client) Checks(ctx context.Context, repository, sha string) (Checks, error) {
	owner, name, err := splitRepository(repository)
	if err != nil {
		return Checks{}, err
	}
	out := Checks{Revision: sha}

	var runs checkRunsResponse
	path := fmt.Sprintf("/repos/%s/%s/commits/%s/check-runs?per_page=100", owner, name, url.PathEscape(sha))
	if err := c.do(ctx, "GET", path, &runs); err != nil {
		return Checks{}, err
	}
	for _, run := range runs.CheckRuns {
		// Runs reported against any other SHA are somebody else's evidence.
		if run.HeadSHA != "" && !strings.EqualFold(run.HeadSHA, sha) {
			continue
		}
		out.Runs = append(out.Runs, CheckRun{
			Name: run.Name, Status: run.Status, Conclusion: run.Conclusion,
			HeadSHA: run.HeadSHA, RunID: run.ID, URL: run.HTMLURL,
			StartedAt: run.StartedAt, CompletedAt: run.CompletedAt,
		})
	}

	var workflows workflowRunsResponse
	path = fmt.Sprintf("/repos/%s/%s/actions/runs?head_sha=%s&per_page=100", owner, name, url.QueryEscape(sha))
	if err := c.do(ctx, "GET", path, &workflows); err != nil {
		return out, nil // check runs alone are still a usable answer
	}
	for _, run := range workflows.WorkflowRuns {
		if run.HeadSHA != "" && !strings.EqualFold(run.HeadSHA, sha) {
			continue
		}
		out.Workflows = append(out.Workflows, WorkflowRun{
			ID: run.ID, Name: run.Name, Status: run.Status, Conclusion: run.Conclusion,
			HeadSHA: run.HeadSHA, URL: run.HTMLURL, FinishedAt: run.UpdatedAt,
		})
	}
	return out, nil
}

// mergeSubject matches the merge commits GitHub writes, which is the only
// place a pull request number appears in a comparison. Pull requests are
// provenance summaries here, so a best-effort read of the subject line is the
// right amount of effort - the commits are the evidence.
var mergeSubject = regexp.MustCompile(`^Merge pull request #(\d+) from (\S+)`)

// squashSubject matches GitHub's squash-merge subject, `Title (#123)`.
var squashSubject = regexp.MustCompile(`^(.*) \(#(\d+)\)$`)

func pullRequestsIn(commits []Revision) []PullRequestSummary {
	var summaries []PullRequestSummary
	seen := map[int]bool{}
	for _, commit := range commits {
		number, title, branch := 0, "", ""
		if m := mergeSubject.FindStringSubmatch(commit.Subject); m != nil {
			// A merge commit's subject names the branch, not the pull
			// request's title - that lives in the body, which a summary does
			// not need. The branch is the provenance.
			number, branch = atoi(m[1]), m[2]
		} else if m := squashSubject.FindStringSubmatch(commit.Subject); m != nil {
			number, title = atoi(m[2]), strings.TrimSpace(m[1])
		}
		if number == 0 || seen[number] {
			continue
		}
		seen[number] = true
		summaries = append(summaries, PullRequestSummary{
			Number: number, Title: title, Merge: commit.SHA, Summary: branch,
		})
	}
	return summaries
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func subjectOf(message string) string {
	if line, _, found := strings.Cut(message, "\n"); found {
		return strings.TrimSpace(line)
	}
	return strings.TrimSpace(message)
}

func splitRepository(repository string) (owner, name string, err error) {
	owner, name, found := strings.Cut(strings.TrimSpace(repository), "/")
	if !found || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", "", fmt.Errorf("github: %q is not owner/name", repository)
	}
	return owner, name, nil
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
