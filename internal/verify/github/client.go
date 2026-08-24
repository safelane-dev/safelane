package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Fetcher fetches Facts about a pull request from GitHub. Verify depends on
// this interface, not on *Client directly, so tests can supply a fixture
// Fetcher instead of talking to the network, and so this seam can be pointed
// at a different GitHub-compatible endpoint (e.g. GitHub Enterprise) without
// changing verification logic.
type Fetcher interface {
	FetchPullRequestFacts(ctx context.Context, owner, repo string, number int) (Facts, error)
}

// Client is the real Fetcher, backed by the GitHub REST API. BaseURL
// defaults to https://api.github.com and can be overridden to point at a
// fixture httptest.Server in tests, or at GitHub Enterprise in production.
type Client struct {
	BaseURL    string // defaults to https://api.github.com
	Token      string // optional; unauthenticated calls work against public repos, rate-limited
	HTTPClient *http.Client
}

// Ping verifies that GitHub is reachable and returns the authenticated login.
// Unlike FetchPullRequestFacts, it verifies no release claim.
func (c *Client) Ping(ctx context.Context) (string, error) {
	var user struct {
		Login string `json:"login"`
	}
	if err := c.do(ctx, http.MethodGet, "/user", &user); err != nil {
		return "", err
	}
	if user.Login == "" {
		return "", fmt.Errorf("github: authenticated user response had no login")
	}
	return user.Login, nil
}

func (c *Client) baseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return "https://api.github.com"
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (c *Client) do(ctx context.Context, method, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL()+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("github: %s %s: unexpected status %d", method, path, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) read(ctx context.Context, path, accept string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL()+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("github: GET %s: unexpected status %d", path, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

var errNotFound = fmt.Errorf("github: not found")

type pullRequestResponse struct {
	Number         int       `json:"number"`
	HTMLURL        string    `json:"html_url"`
	Merged         bool      `json:"merged"`
	MergedAt       time.Time `json:"merged_at"`
	MergeCommitSHA string    `json:"merge_commit_sha"`
	Base           struct {
		Ref string `json:"ref"`
	} `json:"base"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
}

type checkRunsResponse struct {
	CheckRuns []struct {
		ID          int64     `json:"id"`
		Name        string    `json:"name"`
		Status      string    `json:"status"`
		Conclusion  string    `json:"conclusion"`
		HeadSHA     string    `json:"head_sha"`
		HTMLURL     string    `json:"html_url"`
		StartedAt   time.Time `json:"started_at"`
		CompletedAt time.Time `json:"completed_at"`
	} `json:"check_runs"`
}

// FetchPullRequestFacts observes a pull request's merge state, author, and
// the check runs recorded against its merge commit SHA specifically (never the PR head).
func (c *Client) FetchPullRequestFacts(ctx context.Context, owner, repo string, number int) (Facts, error) {
	var pr pullRequestResponse
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, repo, number), &pr); err != nil {
		return Facts{}, err
	}

	facts := Facts{
		Repository:     owner + "/" + repo,
		Number:         pr.Number,
		URL:            pr.HTMLURL,
		Merged:         pr.Merged,
		MergedAt:       pr.MergedAt,
		BaseRef:        pr.Base.Ref,
		MergeCommitSHA: pr.MergeCommitSHA,
		AuthorLogin:    pr.User.Login,
	}

	// Check runs are fetched for the merge commit SHA specifically. If the
	// PR has no merge commit yet (unmerged), skip this call rather than
	// asking the API for check runs on an empty ref.
	if facts.MergeCommitSHA != "" {
		var checks checkRunsResponse
		checkPath := fmt.Sprintf("/repos/%s/%s/commits/%s/check-runs", owner, repo, facts.MergeCommitSHA)
		if err := c.do(ctx, http.MethodGet, checkPath, &checks); err != nil {
			return Facts{}, err
		}
		for _, run := range checks.CheckRuns {
			facts.CheckRuns = append(facts.CheckRuns, CheckRun{
				Name:        run.Name,
				Status:      run.Status,
				Conclusion:  run.Conclusion,
				HeadSHA:     facts.MergeCommitSHA, // the API scopes this call to this exact SHA
				RunID:       run.ID,
				URL:         run.HTMLURL,
				StartedAt:   run.StartedAt,
				CompletedAt: run.CompletedAt,
			})
		}
	}

	return facts, nil
}

// Verify fetches Facts for claim.PullRequestNumber via fetcher and evaluates
// them against claim. Fetch failures (network error, not found, malformed
// response) produce StatusUnknown, never a passing result.
func Verify(ctx context.Context, fetcher Fetcher, claim Claim, owner, repo string) Result {
	facts, err := fetcher.FetchPullRequestFacts(ctx, owner, repo, claim.PullRequestNumber)
	if err != nil {
		if err == errNotFound {
			return unknown(ReasonPullRequestNotFound, "pull request %s/%s#%d not found", owner, repo, claim.PullRequestNumber)
		}
		return unknown(ReasonFetchFailed, "could not fetch pull request %s/%s#%d: %v", owner, repo, claim.PullRequestNumber, err)
	}
	return Evaluate(claim, facts)
}
