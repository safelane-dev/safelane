package oci_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/AndrewMaged814/safelane/internal/release"
	"github.com/AndrewMaged814/safelane/internal/verify/oci"
)

// fakeBinding stands in for the two places outside the image that can prove
// the same thing: a build system's attestation, and SafeLane's own history.
// Both live elsewhere; what is tested here is the resolver's rule about them.
type fakeBinding struct {
	metadata oci.SourceMetadata
	known    bool
	err      error
	asked    int
}

func (f *fakeBinding) Bind(context.Context, oci.Artifact) (oci.SourceMetadata, bool, error) {
	f.asked++
	return f.metadata, f.known, f.err
}

// unlabelledRegistry answers with an image that carries no provenance of its
// own, so the extra binding sources are the only way through.
type unlabelledRegistry struct{ tags []string }

func (r unlabelledRegistry) Resolve(_ context.Context, _, reference string) (string, error) {
	if strings.HasPrefix(reference, "sha256:") {
		return reference, nil
	}
	return "sha256:" + strings.Repeat("a", 64), nil
}

func (r unlabelledRegistry) Platforms(context.Context, string, string) ([]oci.PlatformLabels, error) {
	return []oci.PlatformLabels{{Platform: "linux/amd64", Labels: map[string]string{}}}, nil
}

func (r unlabelledRegistry) Tags(context.Context, string) ([]string, error) { return r.tags, nil }

func artifact() oci.Artifact {
	return oci.Artifact{Repository: "ghcr.io/acme/payments-api", Digest: "sha256:" + strings.Repeat("a", 64)}
}

// Build provenance supplies the same binding as image metadata, and says so.
func TestCIProvenanceCanSupplyTheBinding(t *testing.T) {
	provenance := &fakeBinding{
		metadata: oci.SourceMetadata{
			Source:   "https://github.com/acme/payments-api",
			Revision: revisionA,
			Method:   oci.BindingCIProvenance,
		},
		known: true,
	}
	r := oci.Resolver{Registry: unlabelledRegistry{}, Extra: []oci.BindingSource{provenance}}

	metadata, err := r.ReadSource(context.Background(), artifact())
	if err != nil {
		t.Fatalf("ReadSource: %v", err)
	}
	if metadata.Method != oci.BindingCIProvenance || metadata.Revision != revisionA {
		t.Errorf("metadata = %+v", metadata)
	}
}

// The sources are asked in order and the first that knows wins, so history
// never overrides an attestation that already answered.
func TestBindingSourcesAreAskedInOrder(t *testing.T) {
	provenance := &fakeBinding{
		metadata: oci.SourceMetadata{
			Source: "https://github.com/acme/payments-api", Revision: revisionA, Method: oci.BindingCIProvenance,
		},
		known: true,
	}
	history := &fakeBinding{
		metadata: oci.SourceMetadata{
			Source: "https://github.com/acme/payments-api", Revision: revisionB, Method: oci.BindingHistory,
		},
		known: true,
	}
	r := oci.Resolver{Registry: unlabelledRegistry{}, Extra: []oci.BindingSource{provenance, history}}

	metadata, err := r.ReadSource(context.Background(), artifact())
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Method != oci.BindingCIProvenance {
		t.Errorf("method = %s", metadata.Method)
	}
	if history.asked != 0 {
		t.Error("history was asked after provenance already answered")
	}
}

func TestSafeLaneHistoryCanSupplyTheBinding(t *testing.T) {
	provenance := &fakeBinding{}
	history := &fakeBinding{
		metadata: oci.SourceMetadata{
			Source: "https://github.com/acme/payments-api", Revision: revisionB, Method: oci.BindingHistory,
		},
		known: true,
	}
	r := oci.Resolver{Registry: unlabelledRegistry{}, Extra: []oci.BindingSource{provenance, history}}

	metadata, err := r.ReadSource(context.Background(), artifact())
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Method != oci.BindingHistory {
		t.Errorf("method = %s", metadata.Method)
	}
	if provenance.asked != 1 {
		t.Errorf("provenance asked %d times, want 1", provenance.asked)
	}
}

// A binding source that answers with half a binding is refused, not padded
// out. A revision with no source names a commit in some repository or other.
func TestAPartialBindingIsRefused(t *testing.T) {
	partial := &fakeBinding{
		metadata: oci.SourceMetadata{Revision: revisionA, Method: oci.BindingHistory},
		known:    true,
	}
	r := oci.Resolver{Registry: unlabelledRegistry{}, Extra: []oci.BindingSource{partial}}

	_, err := r.ReadSource(context.Background(), artifact())
	assertRejection(t, err, "incomplete_binding")
}

// A binding that does not say how it knows is a defect, not evidence.
func TestABindingWithNoMethodIsADefect(t *testing.T) {
	anonymous := &fakeBinding{
		metadata: oci.SourceMetadata{Source: "https://github.com/acme/payments-api", Revision: revisionA},
		known:    true,
	}
	r := oci.Resolver{Registry: unlabelledRegistry{}, Extra: []oci.BindingSource{anonymous}}

	_, err := r.ReadSource(context.Background(), artifact())
	assertRejection(t, err, "unrecorded_binding_method")
}

// A short SHA is a prefix, and a prefix is a guess with good odds.
func TestAnAbbreviatedRevisionIsNotARevision(t *testing.T) {
	abbreviated := &fakeBinding{
		metadata: oci.SourceMetadata{
			Source: "https://github.com/acme/payments-api", Revision: revisionA[:7], Method: oci.BindingHistory,
		},
		known: true,
	}
	r := oci.Resolver{Registry: unlabelledRegistry{}, Extra: []oci.BindingSource{abbreviated}}

	_, err := r.ReadSource(context.Background(), artifact())
	assertRejection(t, err, "malformed_source_metadata")
}

// A tag spelled like a commit proves nothing on its own: with no labels and no
// binding source, enumeration finds nothing rather than reading the tag.
func TestARevisionIsNeverInferredFromATagSpelling(t *testing.T) {
	r := oci.Resolver{Registry: unlabelledRegistry{tags: []string{
		"sha-" + revisionA, revisionA, "main-" + revisionA[:7], "latest",
	}}}

	_, err := r.FindRevision(context.Background(), "ghcr.io/acme/payments-api", "", revisionA)
	assertRejection(t, err, "revision_not_published")
}

func TestFindRevisionRequiresAFullSHA(t *testing.T) {
	r := oci.Resolver{Registry: unlabelledRegistry{}}
	_, err := r.FindRevision(context.Background(), "ghcr.io/acme/payments-api", "", "abc1234")
	assertRejection(t, err, "invalid_revision")
}

// The escape hatch, and its one guard: SafeLane confirms the commit exists in
// the registered repository before believing a person about it.
type fakeChecker struct {
	exists     bool
	err        error
	repository string
	revision   string
}

func (f *fakeChecker) RevisionExists(_ context.Context, repository, revision string) (bool, error) {
	f.repository, f.revision = repository, revision
	return f.exists, f.err
}

func TestConfirmedBaselineChecksTheCommitExists(t *testing.T) {
	checker := &fakeChecker{exists: true}
	r := oci.Resolver{Registry: unlabelledRegistry{}}

	metadata, err := r.ConfirmBaseline(context.Background(), checker, artifact(), "acme/payments-api", revisionA)
	if err != nil {
		t.Fatalf("ConfirmBaseline: %v", err)
	}
	if metadata.Method != oci.BindingConfirmedBaseline {
		t.Errorf("method = %s", metadata.Method)
	}
	if metadata.Revision != revisionA {
		t.Errorf("revision = %s", metadata.Revision)
	}
	if checker.repository != "acme/payments-api" || checker.revision != revisionA {
		t.Errorf("checked %s@%s", checker.repository, checker.revision)
	}
}

func TestConfirmedBaselineRefusesACommitThatIsNotThere(t *testing.T) {
	r := oci.Resolver{Registry: unlabelledRegistry{}}
	_, err := r.ConfirmBaseline(context.Background(), &fakeChecker{exists: false},
		artifact(), "acme/payments-api", revisionA)
	assertRejection(t, err, "revision_not_in_repository")
}

func TestConfirmedBaselineRefusesAnAbbreviatedCommit(t *testing.T) {
	r := oci.Resolver{Registry: unlabelledRegistry{}}
	_, err := r.ConfirmBaseline(context.Background(), &fakeChecker{exists: true},
		artifact(), "acme/payments-api", revisionA[:7])
	assertRejection(t, err, "invalid_revision")
}

// GitHub being unreachable is "I do not know", never "it is not there".
func TestConfirmedBaselineReportsAnUnreachableGitHubAsUnknown(t *testing.T) {
	r := oci.Resolver{Registry: unlabelledRegistry{}}
	_, err := r.ConfirmBaseline(context.Background(),
		&fakeChecker{err: errors.New("dial tcp: timeout")},
		artifact(), "acme/payments-api", revisionA)
	assertRejection(t, err, "revision_unknown")
	if !errors.Is(err, release.ErrEvidenceUnknown) {
		t.Errorf("an unreachable GitHub must not read as failed evidence: %v", err)
	}
}

// Every binding says how it knows. "How do you know" is a different question
// from "what do you think", and only one of them can be audited.
func TestEveryBindingMethodIsNamed(t *testing.T) {
	for _, method := range []oci.BindingMethod{
		oci.BindingOCILabels, oci.BindingCIProvenance, oci.BindingHistory, oci.BindingConfirmedBaseline,
	} {
		if method == "" {
			t.Error("a binding method has no name")
		}
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

func assertRemedy(t *testing.T, err error, want string) {
	t.Helper()
	for _, e := range release.Flatten(err) {
		if strings.Contains(e.Remedy, want) {
			return
		}
	}
	t.Errorf("want a remedy containing %q, got %v", want, err)
}
