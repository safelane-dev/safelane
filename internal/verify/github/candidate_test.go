package github_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/AndrewMaged814/safelane/internal/release"
	"github.com/AndrewMaged814/safelane/internal/verify/github"
)

const (
	headSHA     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	olderSHA    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	forkSHA     = "cccccccccccccccccccccccccccccccccccccccc"
	deployedSHA = "dddddddddddddddddddddddddddddddddddddddd"
)

// fakeSource answers from canned facts, so every rule below is tested against
// observations rather than against a network.
type fakeSource struct {
	head        github.Revision
	revisions   map[string]github.Revision
	comparisons map[string]github.Comparison
	checks      map[string]github.Checks
	repository  github.Repository
	headErr     error
	revisionErr error
}

func (f fakeSource) Repository(context.Context, string, string) (github.Repository, error) {
	return f.repository, nil
}

func (f fakeSource) DefaultHead(context.Context, string) (github.Revision, error) {
	return f.head, f.headErr
}

func (f fakeSource) Revision(_ context.Context, _, sha string) (github.Revision, error) {
	if revision, ok := f.revisions[sha]; ok {
		return revision, nil
	}
	if f.revisionErr != nil {
		return github.Revision{}, f.revisionErr
	}
	return github.Revision{}, errors.New("github: not found")
}

func (f fakeSource) Compare(_ context.Context, _, base, head string) (github.Comparison, error) {
	return f.comparisons[base+"..."+head], nil
}

func (f fakeSource) Checks(_ context.Context, _, sha string) (github.Checks, error) {
	return f.checks[sha], nil
}

func defaultSource() fakeSource {
	return fakeSource{
		repository: github.Repository{FullName: "acme/payments-api", DefaultBranch: "main"},
		head:       github.Revision{SHA: headSHA, Subject: "feat: add refunds", OnDefaultBranch: true},
		revisions: map[string]github.Revision{
			headSHA:  {SHA: headSHA, Subject: "feat: add refunds", OnDefaultBranch: true},
			olderSHA: {SHA: olderSHA, Subject: "chore: bump deps", OnDefaultBranch: true},
			forkSHA:  {SHA: forkSHA, Subject: "wip", OnDefaultBranch: false},
		},
	}
}

// With no revision given, the candidate is the head observed now. Re-reading
// it later would silently release something the user never saw.
func TestCandidateDefaultsToTheDefaultBranchHead(t *testing.T) {
	candidate, err := github.SelectCandidate(context.Background(), defaultSource(), "acme/payments-api", "")
	if err != nil {
		t.Fatalf("SelectCandidate: %v", err)
	}
	if candidate.Revision.SHA != headSHA {
		t.Errorf("candidate = %s", candidate.Revision.SHA)
	}
	if candidate.Requested {
		t.Error("a default-branch head was reported as requested")
	}
}

func TestANamedRevisionIsUsedExactly(t *testing.T) {
	candidate, err := github.SelectCandidate(context.Background(), defaultSource(), "acme/payments-api", olderSHA)
	if err != nil {
		t.Fatalf("SelectCandidate: %v", err)
	}
	if candidate.Revision.SHA != olderSHA || !candidate.Requested {
		t.Errorf("candidate = %+v", candidate)
	}
}

// A commit that exists on somebody's fork is a commit that exists, and it is
// not something the registered repository has agreed to ship.
func TestARevisionOffTheDefaultBranchIsRefused(t *testing.T) {
	_, err := github.SelectCandidate(context.Background(), defaultSource(), "acme/payments-api", forkSHA)
	assertRejection(t, err, "revision_not_on_default_branch")
}

func TestARevisionThatIsNotInTheRepositoryIsRefused(t *testing.T) {
	_, err := github.SelectCandidate(context.Background(), defaultSource(), "acme/payments-api",
		"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
	assertRejection(t, err, "revision_not_found")
}

func TestAnAbbreviatedRevisionIsRefused(t *testing.T) {
	_, err := github.SelectCandidate(context.Background(), defaultSource(), "acme/payments-api", headSHA[:7])
	assertRejection(t, err, "invalid_revision")
}

// GitHub being unreachable is "I do not know", never "there is no head".
func TestAnUnreachableGitHubIsUnknownEvidence(t *testing.T) {
	source := defaultSource()
	source.headErr = errors.New("dial tcp: timeout")

	_, err := github.SelectCandidate(context.Background(), source, "acme/payments-api", "")
	assertRejection(t, err, "github_unreachable")
	if !errors.Is(err, release.ErrEvidenceUnknown) {
		t.Errorf("an unreachable GitHub must not read as failed evidence: %v", err)
	}
}

func assertRejection(t *testing.T, err error, code string) {
	t.Helper()
	for _, e := range release.Flatten(err) {
		if e.Code == code {
			return
		}
	}
	t.Errorf("want a rejection with code %q, got %v", code, err)
}

func assertNoBlocker(t *testing.T, result github.Eligibility, code string) {
	t.Helper()
	for _, blocker := range result.Blockers {
		if blocker.Code == code {
			t.Errorf("unexpected blocker %q: %s", code, blocker.Reason)
		}
	}
}

func assertBlocker(t *testing.T, result github.Eligibility, code string) github.Blocker {
	t.Helper()
	for _, blocker := range result.Blockers {
		if blocker.Code == code {
			if blocker.Reason == "" || blocker.Remedy == "" {
				t.Errorf("blocker %q has nothing to act on: %+v", code, blocker)
			}
			return blocker
		}
	}
	t.Errorf("no blocker %q; got %s", code, blockerCodes(result))
	return github.Blocker{}
}

func blockerCodes(result github.Eligibility) string {
	codes := make([]string, 0, len(result.Blockers))
	for _, blocker := range result.Blockers {
		codes = append(codes, blocker.Code)
	}
	return strings.Join(codes, ", ")
}
