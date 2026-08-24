package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AndrewMaged814/safelane/internal/config"
	"github.com/AndrewMaged814/safelane/internal/delta"
	"github.com/AndrewMaged814/safelane/internal/discovery"
	"github.com/AndrewMaged814/safelane/internal/release"
	"github.com/AndrewMaged814/safelane/internal/verify/github"
	"github.com/AndrewMaged814/safelane/internal/verify/oci"
)

const (
	runningRevision   = "dddddddddddddddddddddddddddddddddddddddd"
	candidateRevision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	runningDigest     = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	candidateDigest   = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	// The one string that must never appear anywhere SafeLane writes.
	liveSecret = "sk_live_NEVER_CAPTURED"
)

// deployedRollout carries an environment value on purpose: the boundary has to
// drop it, and a fixture with nothing to drop proves nothing.
func deployedRollout() string {
	return `{
      "metadata": {"name": "payments-api", "namespace": "payments"},
      "spec": {
        "replicas": 4,
        "strategy": {"canary": {
          "stableService": "payments-api-stable",
          "canaryService": "payments-api-canary",
          "analysis": {"templates": [{"templateName": "success-rate"}]}
        }},
        "template": {"spec": {
          "containers": [{
            "name": "payments-api",
            "image": "ghcr.io/acme/payments-api@` + runningDigest + `",
            "env": [
              {"name": "LOG_LEVEL", "value": "info"},
              {"name": "STRIPE_SECRET_KEY", "value": "` + liveSecret + `"}
            ],
            "envFrom": [{"secretRef": {"name": "payments-api-secrets"}}]
          }]
        }}
      }
    }`
}

func inspectCluster() fakeCluster {
	return fakeCluster{
		"config current-context":                                    "safelane-caller-payments-api",
		"get rollouts.argoproj.io payments-api -n payments -o json": deployedRollout(),
		"get service payments-api-stable -n payments -o json":       `{"metadata":{"name":"payments-api-stable"}}`,
		"get service payments-api-canary -n payments -o json":       `{"metadata":{"name":"payments-api-canary"}}`,
		"get analysistemplate success-rate -o json -n payments": `{
          "metadata": {"name": "success-rate", "namespace": "payments"},
          "spec": {"metrics": [{
            "name": "success-rate", "interval": "30s", "initialDelay": "60s",
            "successCondition": "len(result) > 0 && result[0] >= 0.99",
            "failureLimit": 1,
            "provider": {"prometheus": {"address": "http://prometheus:9090", "query": "up"}}
          }]}
        }`,
	}
}

// inspectRegistry answers the two images this release is about.
type inspectRegistry struct{}

func (inspectRegistry) Resolve(_ context.Context, _, reference string) (string, error) {
	switch reference {
	case "sha-old", runningDigest:
		return runningDigest, nil
	case "sha-new", candidateDigest:
		return candidateDigest, nil
	}
	return "", fmt.Errorf("no such reference %q", reference)
}

func (inspectRegistry) Platforms(_ context.Context, _, digest string) ([]oci.PlatformLabels, error) {
	revision := runningRevision
	if digest == candidateDigest {
		revision = candidateRevision
	}
	return []oci.PlatformLabels{{Platform: "linux/amd64", Labels: map[string]string{
		"org.opencontainers.image.source":   "https://github.com/acme/payments-api",
		"org.opencontainers.image.revision": revision,
	}}}, nil
}

func (inspectRegistry) Tags(context.Context, string) ([]string, error) {
	return []string{"sha-old", "sha-new"}, nil
}

// inspectSource answers what GitHub would.
type inspectSource struct {
	protection github.Repository
	checks     github.Checks
	comparison github.Comparison
	err        error
}

func (s inspectSource) Repository(context.Context, string, string) (github.Repository, error) {
	if s.err != nil {
		return github.Repository{}, s.err
	}
	return s.protection, nil
}

func (s inspectSource) DefaultHead(context.Context, string) (github.Revision, error) {
	return github.Revision{
		SHA: candidateRevision, Subject: "docs: correct a typo in the refund guide",
		OnDefaultBranch: true, CommittedAt: time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC),
	}, nil
}

func (s inspectSource) Revision(_ context.Context, _, sha string) (github.Revision, error) {
	return github.Revision{SHA: sha, OnDefaultBranch: true}, nil
}

func (s inspectSource) Compare(context.Context, string, string, string) (github.Comparison, error) {
	return s.comparison, nil
}

func (s inspectSource) Checks(context.Context, string, string) (github.Checks, error) {
	return s.checks, nil
}

func defaultInspectSource() inspectSource {
	return inspectSource{
		protection: github.Repository{
			FullName: "acme/payments-api", DefaultBranch: "main",
			Protected: true, RequiredChecks: []string{"build-and-push"},
		},
		checks: github.Checks{Revision: candidateRevision, Runs: []github.CheckRun{
			{Name: "build-and-push", Status: "completed", Conclusion: "success", HeadSHA: candidateRevision},
		}, Workflows: []github.WorkflowRun{
			{ID: 42, Name: "build-and-push", Status: "completed", Conclusion: "success", HeadSHA: candidateRevision},
		}},
		comparison: github.Comparison{
			Base: runningRevision, Head: candidateRevision, Status: "ahead", AheadBy: 2,
			Commits: []github.Revision{
				{SHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Subject: "docs: clarify the refund example"},
				{SHA: candidateRevision, Subject: "docs: correct a typo in the refund guide"},
			},
			Files: []github.FileChange{
				{Path: "docs/refunds.md", Status: "modified", Additions: 1, Deletions: 1},
			},
		},
	}
}

func registeredHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	file := config.Render(config.Discovered{
		Application: config.Application{Name: "payments-api", Repository: "acme/payments-api"},
		Artifact:    config.Artifact{Container: "payments-api", Image: "ghcr.io/acme/payments-api"},
		Environment: config.Environment{
			Name: "production", Impact: config.ImpactCritical,
			Kubernetes: config.Kubernetes{
				Context: "safelane-caller-payments-api", Namespace: "payments", Rollout: "payments-api",
			},
		},
	}, config.DefaultReleaseSettings())
	if _, err := config.Write(config.ForApp(home, "payments-api").File, file); err != nil {
		t.Fatal(err)
	}
	return home
}

func inspectOptions(t *testing.T) InspectOptions {
	t.Helper()
	cluster := inspectCluster()
	home := registeredHome(t)
	confirmationPath := filepath.Join(config.ForApp(home, "payments-api").ForEnvironment("production").Dir, confirmedBuildFile)
	if err := saveConfirmedBuild(confirmationPath, confirmedBuild{
		Candidate: candidateRevision, Digest: candidateDigest, RunID: 42, RunName: "build-and-push",
	}); err != nil {
		t.Fatal(err)
	}
	return InspectOptions{
		Root:        ".",
		Home:        home,
		Environment: "production",
		Cluster: discovery.Service{
			Run:    cluster.run,
			Origin: func(string) (string, error) { return "acme/payments-api", nil },
		},
		Source:   defaultInspectSource(),
		Registry: oci.Resolver{Registry: inspectRegistry{}},
		Now:      func() time.Time { return time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC) },
	}
}

func TestInspectFreezesADeltaAndReportsFourViews(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Inspect(context.Background(), inspectOptions(t), &terminal{&stdout}, &stderr)
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "snapshot sha256:") {
		t.Errorf("no snapshot id:\n%s", out)
	}
	for _, name := range delta.ViewNames {
		if !strings.Contains(out, "== "+name+" ==") {
			t.Errorf("view %q is missing:\n%s", name, out)
		}
	}
	for _, want := range []string{
		"2 commits", "docs/refunds.md",
		"production (critical impact)", `Rollout "payments-api"`,
		"success-rate", "Prometheus", "result[0] >= 0.99",
		"no previous SafeLane release",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
}

func TestInspectionContentAddressesTheCompleteRawDiff(t *testing.T) {
	opts := inspectOptions(t)
	source := defaultInspectSource()
	source.comparison.Diff = []byte("diff --git a/internal/refunds.go b/internal/refunds.go\n+func Refund() {}\n")
	opts.Source = source
	frozen, _, err := FreezeDelta(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(frozen.Changes().Diffs) != 1 || frozen.Changes().Diffs[0].Kind != "diff" {
		t.Fatalf("raw diff handle = %+v", frozen.Changes().Diffs)
	}
}

type diffFixture struct {
	content []byte
	err     error
}

func (f diffFixture) RawDiff(context.Context, string, string, string) ([]byte, error) {
	return f.content, f.err
}

func TestEvidenceResolvesAndVerifiesTheFrozenRawDiff(t *testing.T) {
	opts := inspectOptions(t)
	source := defaultInspectSource()
	diff := []byte("diff --git a/internal/refunds.go b/internal/refunds.go\n+func Refund() {}\n")
	source.comparison.Diff = diff
	opts.Source = source
	frozen, _, err := FreezeDelta(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	handle := frozen.Changes().Diffs[0]

	var stdout, stderr bytes.Buffer
	code := Evidence(context.Background(), EvidenceOptions{
		Root: ".", Home: opts.Home, Environment: "production", HandleID: handle.ID,
		Origin: func(string) (string, error) { return "acme/payments-api", nil },
		Source: diffFixture{content: diff},
	}, &stdout, &stderr)
	if code != ExitOK || !bytes.Equal(stdout.Bytes(), diff) {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout.Bytes(), stderr.String())
	}
}

func TestEvidenceRefusesBytesThatDoNotMatchTheFrozenHandle(t *testing.T) {
	opts := inspectOptions(t)
	source := defaultInspectSource()
	source.comparison.Diff = []byte("original diff")
	opts.Source = source
	frozen, _, err := FreezeDelta(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := Evidence(context.Background(), EvidenceOptions{
		Root: ".", Home: opts.Home, Environment: "production", HandleID: frozen.Changes().Diffs[0].ID,
		Origin: func(string) (string, error) { return "acme/payments-api", nil },
		Source: diffFixture{content: []byte("changed diff")},
	}, &stdout, &stderr)
	if code == ExitOK || stdout.Len() != 0 || !strings.Contains(stderr.String(), "does not match what was frozen") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
}

func TestFreezeDeltaDoesNotHideSourceEvidenceFailures(t *testing.T) {
	opts := inspectOptions(t)
	source := defaultInspectSource()
	source.err = errors.New("GitHub unavailable")
	opts.Source = source
	_, _, err := FreezeDelta(context.Background(), opts)
	if err == nil || !errors.Is(err, release.ErrEvidenceUnknown) {
		t.Fatalf("error = %v, want unknown evidence", err)
	}
}

// The whole point of exclusion at capture: the value is not anywhere SafeLane
// writes, and the check for that is a substring search.
func TestInspectNeverPrintsASecretValue(t *testing.T) {
	for _, piped := range []bool{true, false} {
		var stdout, stderr bytes.Buffer
		var out interface {
			String() string
		} = &stdout

		opts := inspectOptions(t)
		if piped {
			if code := Inspect(context.Background(), opts, &stdout, &stderr); code != ExitOK {
				t.Fatalf("exit: %s", stderr.String())
			}
		} else {
			if code := Inspect(context.Background(), opts, &terminal{&stdout}, &stderr); code != ExitOK {
				t.Fatalf("exit: %s", stderr.String())
			}
		}
		if strings.Contains(out.String(), liveSecret) {
			t.Fatalf("a secret value reached the output (piped=%t)", piped)
		}
		// The reference name did survive: a name is enough for a deployment
		// observation.
		if !strings.Contains(out.String(), "payments-api-secrets") {
			t.Errorf("the reference name did not survive (piped=%t)", piped)
		}
	}
}

func TestInspectIsJSONWhenPiped(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Inspect(context.Background(), inspectOptions(t), &stdout, &stderr); code != ExitOK {
		t.Fatalf("exit: %s", stderr.String())
	}
	var result struct {
		SnapshotID string            `json:"snapshot_id"`
		Views      map[string]string `json:"views"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("piped output is not JSON: %v\n%s", err, stdout.String())
	}
	if !strings.HasPrefix(result.SnapshotID, "sha256:") {
		t.Errorf("snapshot_id = %q", result.SnapshotID)
	}
	if len(result.Views) != 4 {
		t.Errorf("views = %v", result.Views)
	}
}

// Freezing the same release twice at different clock times gives the same
// snapshot, so "the assessment saw exactly this" is checkable.
func TestInspectingTwiceFreezesTheSameSnapshot(t *testing.T) {
	opts := inspectOptions(t)
	first, _, err := FreezeDelta(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.Now = func() time.Time { return time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC) }
	second, _, err := FreezeDelta(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if first.SnapshotID() != second.SnapshotID() {
		t.Errorf("snapshots differ:\n  %s\n  %s", first.SnapshotID(), second.SnapshotID())
	}
}

// An ineligible release stops before assessment, and says why in eligibility
// language rather than as a risk level.
func TestInspectStopsBeforeAssessmentWhenNotEligible(t *testing.T) {
	opts := inspectOptions(t)
	source := defaultInspectSource()
	source.checks.Runs[0].Conclusion = "failure"
	opts.Source = source

	var stdout, stderr bytes.Buffer
	code := Inspect(context.Background(), opts, &terminal{&stdout}, &stderr)
	if code == ExitOK {
		t.Fatalf("a failed required check did not stop the release:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "This release cannot go ahead yet.") {
		t.Errorf("stderr = %q", stderr.String())
	}
	if strings.Contains(strings.ToLower(stderr.String()), "risk") {
		t.Errorf("an eligibility failure was described as risk: %s", stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "" {
		t.Errorf("evidence was printed for a release that cannot proceed:\n%s", stdout.String())
	}
}

func TestInspectRefusesAnUnregisteredEnvironment(t *testing.T) {
	opts := inspectOptions(t)
	opts.Environment = "staging"

	var stdout, stderr bytes.Buffer
	if code := Inspect(context.Background(), opts, &stdout, &stderr); code == ExitOK {
		t.Fatal("inspect accepted an unregistered environment")
	}
	if !strings.Contains(stderr.String(), "staging is not a registered environment") {
		t.Errorf("the refusal should name the environment: %s", stderr.String())
	}
}

// The proposal starts from the cautious configured lane, and the view says
// which one it is.
func TestTheProposedPatchNamesItsLane(t *testing.T) {
	frozen, _, err := FreezeDelta(context.Background(), inspectOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	patch := frozen.Deployment().Patch
	if patch.Lane != "guarded" {
		t.Errorf("lane = %q, want the cautious default", patch.Lane)
	}
	if len(patch.Weights) != 4 || patch.Weights[3] != 100 {
		t.Errorf("weights = %v", patch.Weights)
	}
	if !strings.Contains(frozen.DeploymentView(), "guarded lane") {
		t.Errorf("deployment view:\n%s", frozen.DeploymentView())
	}
	for _, mapping := range []string{"low    -> fast", "medium -> standard", "high   -> guarded"} {
		if !strings.Contains(frozen.DeploymentView(), mapping) {
			t.Errorf("deployment view is missing %q:\n%s", mapping, frozen.DeploymentView())
		}
	}
}

// Exposure is described as approximate, because it is. Calling a replica ratio
// a traffic percentage would overstate what the canary proved.
func TestExposureIsDescribedHonestly(t *testing.T) {
	frozen, _, err := FreezeDelta(context.Background(), inspectOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(frozen.DeploymentView(), "approximated by replica count") {
		t.Errorf("deployment view:\n%s", frozen.DeploymentView())
	}
}
