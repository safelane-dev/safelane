package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fixtureServer serves canned GitHub API responses for one pull request,
// matching the real shapes of GET .../pulls/{n} and .../commits/{sha}/check-runs,
// so Client can be tested against real-looking
// endpoints without a network dependency.
func fixtureServer(t *testing.T, pullBody, reviewsBody, checksBody string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/safelane-demo-api/pulls/42", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(pullBody))
	})
	mux.HandleFunc("/repos/acme/safelane-demo-api/commits/merge-sha-1/check-runs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(checksBody))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

const fixturePull = `{
	"number": 42,
	"html_url": "https://github.com/acme/safelane-demo-api/pull/42",
	"merged": true,
	"merged_at": "2026-08-10T11:00:00Z",
	"merge_commit_sha": "merge-sha-1",
	"base": {"ref": "main"},
	"user": {"login": "andrew"}
}`

const fixtureChecks = `{
	"check_runs": [
		{"id": 999, "name": "publish", "conclusion": "success", "head_sha": "merge-sha-1", "html_url": "https://github.com/acme/safelane-demo-api/actions/runs/999", "completed_at": "2026-08-10T10:55:00Z"}
	]
}`

func TestClient_FetchPullRequestFacts_RealShapedResponses(t *testing.T) {
	srv := fixtureServer(t, fixturePull, "", fixtureChecks)
	client := &Client{BaseURL: srv.URL}

	facts, err := client.FetchPullRequestFacts(context.Background(), "acme", "safelane-demo-api", 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if facts.Repository != "acme/safelane-demo-api" || !facts.Merged || facts.MergeCommitSHA != "merge-sha-1" {
		t.Fatalf("unexpected facts: %+v", facts)
	}
	if facts.MergedAt.IsZero() || facts.URL == "" {
		t.Fatalf("want merged_at and html_url populated, got %+v", facts)
	}
	if len(facts.CheckRuns) != 1 || facts.CheckRuns[0].RunID != 999 || facts.CheckRuns[0].CompletedAt.IsZero() {
		t.Fatalf("want check run id/completed_at populated, got %+v", facts.CheckRuns)
	}
	if len(facts.CheckRuns) != 1 || facts.CheckRuns[0].Conclusion != "success" {
		t.Fatalf("unexpected check runs: %+v", facts.CheckRuns)
	}
}

func TestClient_FetchPullRequestFacts_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/safelane-demo-api/pulls/999", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := &Client{BaseURL: srv.URL}

	_, err := client.FetchPullRequestFacts(context.Background(), "acme", "safelane-demo-api", 999)
	if err != errNotFound {
		t.Fatalf("want errNotFound, got %v", err)
	}
}

func TestVerify_EndToEnd_UsesRealFetcher(t *testing.T) {
	srv := fixtureServer(t, fixturePull, "", fixtureChecks)
	client := &Client{BaseURL: srv.URL}

	got := Verify(context.Background(), client, baseClaim(), "acme", "safelane-demo-api")
	if got.Status != StatusVerified {
		t.Fatalf("want Verified, got %+v", got)
	}
}

func TestVerify_FetchFailure_IsUnknownNeverPassing(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/safelane-demo-api/pulls/42", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := &Client{BaseURL: srv.URL}

	got := Verify(context.Background(), client, baseClaim(), "acme", "safelane-demo-api")
	if got.Status != StatusUnknown {
		t.Fatalf("want Unknown on fetch failure, got %+v", got)
	}
}

func TestVerify_NotFound_IsUnknownWithReason(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/safelane-demo-api/pulls/42", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := &Client{BaseURL: srv.URL}

	got := Verify(context.Background(), client, baseClaim(), "acme", "safelane-demo-api")
	if got.Status != StatusUnknown || got.Reason != ReasonPullRequestNotFound {
		t.Fatalf("want Unknown/PullRequestNotFound, got %+v", got)
	}
}
