package delta_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/AndrewMaged814/safelane/internal/delta"
)

// rolloutWithSecrets is a pod template carrying every way a value can reach a
// container: a literal env value, a Secret key reference, a ConfigMap key
// reference, envFrom, a mounted Secret, a mounted ConfigMap, and a registry
// credential.
const rolloutWithSecrets = `{
  "metadata": {"name": "payments-api"},
  "spec": {
    "replicas": 4,
    "template": {"spec": {
      "containers": [{
        "name": "payments-api",
        "image": "ghcr.io/acme/payments-api@sha256:abc",
        "env": [
          {"name": "LOG_LEVEL", "value": "info"},
          {"name": "STRIPE_SECRET_KEY", "value": "sk_live_TOPSECRETVALUE"},
          {"name": "DB_PASSWORD", "valueFrom": {"secretKeyRef": {"name": "payments-db", "key": "password"}}},
          {"name": "REGION", "valueFrom": {"configMapKeyRef": {"name": "payments-settings", "key": "region"}}}
        ],
        "envFrom": [
          {"secretRef": {"name": "payments-api-secrets"}},
          {"configMapRef": {"name": "payments-api-config"}}
        ]
      }],
      "volumes": [
        {"name": "tls", "secret": {"secretName": "payments-tls"}},
        {"name": "settings", "configMap": {"name": "payments-settings"}}
      ],
      "imagePullSecrets": [{"name": "ghcr-pull"}]
    }}
  }
}`

// The literal value is the one thing that must never come out. Everything else
// here is a name, and a name is enough for a deployment observation.
const literalSecret = "sk_live_TOPSECRETVALUE"

func TestOnlyReferenceNamesCrossTheBoundary(t *testing.T) {
	references := delta.SecretReferencesIn([]byte(rolloutWithSecrets))

	want := []string{
		"ConfigMap/payments-api-config",
		"ConfigMap/payments-settings",
		"Secret/ghcr-pull",
		"Secret/payments-api-secrets",
		"Secret/payments-db",
		"Secret/payments-tls",
	}
	if strings.Join(references, ",") != strings.Join(want, ",") {
		t.Errorf("references = %v\nwant %v", references, want)
	}
	for _, reference := range references {
		if strings.Contains(reference, literalSecret) {
			t.Fatalf("a value crossed the boundary: %q", reference)
		}
	}
}

// The test for exclusion-at-capture is "is the string anywhere in the frozen
// delta", and that is a question with a real answer. Redaction at render time
// could not be tested this way, which is the reason it is not what SafeLane
// does.
func TestNoSecretValueIsAnywhereInTheFrozenDelta(t *testing.T) {
	in := input()
	in.Deployment.SecretReferences = delta.SecretReferencesIn([]byte(rolloutWithSecrets))
	in.Deployment.Replicas = delta.ReplicasIn([]byte(rolloutWithSecrets))

	frozen := delta.Freeze(in)

	raw, err := json.Marshal(frozen)
	if err != nil {
		t.Fatal(err)
	}
	haystacks := map[string]string{"serialized delta": string(raw)}
	for name, view := range frozen.Views() {
		haystacks["the "+name+" view"] = view
	}
	for where, haystack := range haystacks {
		if strings.Contains(haystack, literalSecret) {
			t.Errorf("a secret value reached %s", where)
		}
	}

	// The names did survive, because a deployment observation needs them.
	if !strings.Contains(frozen.DeploymentView(), "Secret/payments-db") {
		t.Errorf("the reference names did not survive:\n%s", frozen.DeploymentView())
	}
	if frozen.Deployment().Replicas != 4 {
		t.Errorf("replicas = %d", frozen.Deployment().Replicas)
	}
}

// Anything carrying an `=` or a newline is content, not a reference, and
// content does not belong at the boundary.
func TestSomethingThatLooksLikeAValueIsNotAReference(t *testing.T) {
	in := input()
	in.Deployment.SecretReferences = []string{
		"Secret/payments-db",
		"STRIPE_SECRET_KEY=" + literalSecret,
		"Secret/payments-db",
		"",
	}

	frozen := delta.Freeze(in)
	references := frozen.Deployment().SecretReferences
	if len(references) != 1 || references[0] != "Secret/payments-db" {
		t.Errorf("references = %v", references)
	}
	if strings.Contains(frozen.DeploymentView(), literalSecret) {
		t.Error("a value survived as a reference")
	}
}

// A diff hunk touching something this workload actually mounts is reduced to
// the path and the reference name. The rule is narrow on purpose: it is not a
// scanner, and a broader one would quietly hide ordinary changes.
func TestADiffTouchingAKnownSecretReferenceIsReduced(t *testing.T) {
	in := input()
	in.Deployment.SecretReferences = delta.SecretReferencesIn([]byte(rolloutWithSecrets))
	in.Changes.Files = []delta.File{
		{Path: "deploy/payments-tls.yaml", Status: "modified", Additions: 3, Deletions: 1},
		{Path: "internal/refunds.go", Status: "added", Additions: 64, Deletions: 12},
	}

	frozen := delta.Freeze(in)
	files := frozen.Changes().Files
	if files[0].SecretReference != "Secret/payments-tls" {
		t.Errorf("the secret-touching file was not reduced: %+v", files[0])
	}
	if files[1].SecretReference != "" {
		t.Errorf("an ordinary file was reduced: %+v", files[1])
	}

	view := frozen.ChangesView()
	if !strings.Contains(view, "touches Secret/payments-tls (contents not captured)") {
		t.Errorf("changes view:\n%s", view)
	}
	if !strings.Contains(view, "internal/refunds.go") {
		t.Errorf("an ordinary file stopped being shown:\n%s", view)
	}
}

// Handles are content-addressed, so "load it on demand" cannot mean "load
// whatever is there now".
func TestHandlesNameBytesNotPaths(t *testing.T) {
	content := []byte("diff --git a/internal/refunds.go b/internal/refunds.go\n+func Refund() {}\n")
	handle := delta.NewHandle("diff", content, "internal/refunds.go")

	if !strings.HasPrefix(handle.ID, "diff:sha256:") {
		t.Errorf("handle = %q", handle.ID)
	}
	if handle.Bytes != len(content) {
		t.Errorf("bytes = %d", handle.Bytes)
	}
	if err := handle.Verify(content); err != nil {
		t.Errorf("Verify on the same bytes: %v", err)
	}
	if err := handle.Verify([]byte("something else")); err == nil {
		t.Error("Verify accepted different bytes")
	}
}

// Typed detailed evidence is named in the Delta and fetched only when a
// specific question needs it, so the normal path costs one read.
func TestHeavyEvidenceLoadsOnlyOnDemand(t *testing.T) {
	body := []byte("apiVersion: argoproj.io/v1alpha1\nkind: AnalysisTemplate\n")
	analysisHandle := delta.NewHandle("analysis", body, "success-rate as written")

	in := input()
	in.Health[0].Body = &analysisHandle

	frozen := delta.Freeze(in)

	// The views name them and do not contain them.
	if strings.Contains(frozen.HealthView(), "apiVersion") {
		t.Error("the analysis body was carried in the view")
	}
	if !strings.Contains(frozen.HealthView(), analysisHandle.ID) {
		t.Errorf("the health view does not name the handle:\n%s", frozen.HealthView())
	}
	if got := frozen.Handles(); len(got) != 1 {
		t.Errorf("handles = %+v", got)
	}

	// Fetching is somebody else's job, and it is counted.
	fetcher := &countingFetcher{content: map[string][]byte{analysisHandle.ID: body}}
	fetched, err := fetcher.Fetch(context.Background(), analysisHandle)
	if err != nil {
		t.Fatal(err)
	}
	if err := analysisHandle.Verify(fetched); err != nil {
		t.Errorf("fetched evidence does not match what was frozen: %v", err)
	}
	if fetcher.calls != 1 {
		t.Errorf("fetches = %d", fetcher.calls)
	}
}

type countingFetcher struct {
	content map[string][]byte
	calls   int
}

func (f *countingFetcher) Fetch(_ context.Context, handle delta.Handle) ([]byte, error) {
	f.calls++
	return f.content[handle.ID], nil
}
