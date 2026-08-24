package releasepatch_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/AndrewMaged814/safelane/internal/release"
	"github.com/AndrewMaged814/safelane/internal/releasepatch"
)

// richRollout is deliberately full of things SafeLane must not touch:
// probes, resources, replicas, environment, secrets, ports, a traffic router,
// a background analysis, and the application's own labels and annotations.
//
// The logging sidecar is first on purpose. A patch that assumed index 0 would
// release the application's image into it.
const richRollout = `{
  "apiVersion": "argoproj.io/v1alpha1",
  "kind": "Rollout",
  "metadata": {
    "name": "payments-api",
    "namespace": "payments",
    "uid": "9f1c0b52-7a3e-4a1f-9d2e-1c8b6f0a4d33",
    "resourceVersion": "84213",
    "generation": 7,
    "labels": {"app.kubernetes.io/name": "payments-api", "team": "payments"},
    "annotations": {"owner": "payments@acme.example"}
  },
  "spec": {
    "replicas": 6,
    "revisionHistoryLimit": 10,
    "strategy": {
      "canary": {
        "stableService": "payments-api-stable",
        "canaryService": "payments-api-canary",
        "trafficRouting": {"nginx": {"stableIngress": "payments-api"}},
        "analysis": {"templates": [{"templateName": "success-rate"}], "args": [{"name": "svc", "value": "payments-api-canary"}]},
        "steps": [{"setWeight": 10}, {"pause": {"duration": "30s"}}, {"setWeight": 20}, {"pause": {}}]
      }
    },
    "selector": {"matchLabels": {"app.kubernetes.io/name": "payments-api"}},
    "template": {
      "metadata": {"labels": {"app.kubernetes.io/name": "payments-api"}},
      "spec": {
        "imagePullSecrets": [{"name": "ghcr-pull"}],
        "volumes": [{"name": "tls", "secret": {"secretName": "payments-tls"}}],
        "containers": [
          {
            "name": "logging-sidecar",
            "image": "ghcr.io/acme/fluent@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
            "resources": {"requests": {"cpu": "10m"}}
          },
          {
            "name": "payments-api",
            "image": "ghcr.io/acme/payments-api@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
            "ports": [{"name": "http", "containerPort": 8080}],
            "env": [{"name": "LOG_LEVEL", "value": "info"}],
            "envFrom": [{"secretRef": {"name": "payments-api-secrets"}}],
            "readinessProbe": {"httpGet": {"path": "/readyz", "port": "http"}, "periodSeconds": 10},
            "livenessProbe": {"httpGet": {"path": "/healthz", "port": "http"}},
            "resources": {"requests": {"cpu": "100m", "memory": "64Mi"}, "limits": {"cpu": "500m"}},
            "volumeMounts": [{"name": "tls", "mountPath": "/etc/tls"}]
          }
        ]
      }
    }
  },
  "status": {"currentStepIndex": 3, "replicas": 6}
}`

const (
	previousImage  = "ghcr.io/acme/payments-api@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	candidateImage = "ghcr.io/acme/payments-api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func build(t *testing.T, weights ...int) releasepatch.Patch {
	t.Helper()
	if len(weights) == 0 {
		weights = []int{25, 50, 100}
	}
	patch, err := releasepatch.Build([]byte(richRollout), "payments-api", candidateImage, "standard", weights)
	if err != nil {
		t.Fatal(err)
	}
	return patch
}

// Exactly two paths, and the two tests that guard them.
func TestThePatchTouchesTwoPathsAndNothingElse(t *testing.T) {
	patch := build(t)

	if len(patch.Operations) != 4 {
		t.Fatalf("operations = %d, want 4:\n%+v", len(patch.Operations), patch.Operations)
	}
	want := []struct{ op, path string }{
		{"test", "/metadata/resourceVersion"},
		{"test", "/spec/template/spec/containers/1/image"},
		{"replace", "/spec/template/spec/containers/1/image"},
		{"replace", "/spec/strategy/canary/steps"},
	}
	for i, expected := range want {
		got := patch.Operations[i]
		if got.Op != expected.op || got.Path != expected.path {
			t.Errorf("operation %d = %s %s, want %s %s", i, got.Op, got.Path, expected.op, expected.path)
		}
	}
}

// The container is found by name, at whatever index it occupies. A stored
// index would be a stored assumption about a list somebody else maintains, and
// the failure mode is releasing the application into its logging sidecar.
func TestTheContainerIsFoundByNameNotByPosition(t *testing.T) {
	patch := build(t)
	if patch.ContainerIndex != 1 {
		t.Errorf("container index = %d, want 1", patch.ContainerIndex)
	}
	if patch.PreviousImage != previousImage {
		t.Errorf("previous image = %q", patch.PreviousImage)
	}

	_, err := releasepatch.Build([]byte(richRollout), "worker", candidateImage, "standard", []int{50, 100})
	assertRejection(t, err, "container_not_found")
}

// Each weight but the last becomes a set-weight and an indefinite pause. The
// last weight is not a step: Argo reaches full traffic by running out of steps.
func TestWeightsBecomeAlternatingStepsAndIndefinitePauses(t *testing.T) {
	steps, err := releasepatch.Steps([]int{25, 50, 100})
	if err != nil {
		t.Fatal(err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(steps, &decoded); err != nil {
		t.Fatal(err)
	}

	if len(decoded) != 4 {
		t.Fatalf("steps = %d, want 4:\n%s", len(decoded), steps)
	}
	if decoded[0]["setWeight"] != float64(25) || decoded[2]["setWeight"] != float64(50) {
		t.Errorf("weights = %s", steps)
	}
	for _, i := range []int{1, 3} {
		pause, ok := decoded[i]["pause"].(map[string]any)
		if !ok {
			t.Fatalf("step %d is not a pause: %s", i, steps)
		}
		// An empty pause is indefinite. A pause with a duration resumes on its
		// own, and a rollout that widens because a clock ran out is a rollout
		// nobody decided to widen.
		if len(pause) != 0 {
			t.Errorf("step %d pauses for %v; every pause must be indefinite", i, pause)
		}
	}
	// 100 is not among the setWeight values: it is reached by promotion.
	if strings.Contains(string(steps), "100") {
		t.Errorf("the final weight became a step: %s", steps)
	}
}

func TestALaneMustEndAtOneHundred(t *testing.T) {
	_, err := releasepatch.Steps([]int{25, 50, 75})
	assertRejection(t, err, "lane_does_not_finish")
}

// The boundary is checked against the real before-and-after, so a field this
// package has never heard of is still protected.
func TestARoundTripLeavesEveryUnrelatedFieldIdentical(t *testing.T) {
	patch := build(t)
	after := apply(t, richRollout, patch)

	if err := patch.Verify([]byte(richRollout), after); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	// And the two things that were meant to change, did.
	var result map[string]any
	if err := json.Unmarshal(after, &result); err != nil {
		t.Fatal(err)
	}
	containers := result["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)
	if got := containers[1].(map[string]any)["image"]; got != candidateImage {
		t.Errorf("image = %v", got)
	}
	if got := containers[0].(map[string]any)["image"]; got == candidateImage {
		t.Error("the sidecar was released")
	}
}

// Verify is what makes the boundary a property rather than a promise: it
// notices anything else that moved, and names it.
func TestVerifyNoticesAnythingElseThatChanged(t *testing.T) {
	patch := build(t)
	for name, mutation := range map[string]struct{ from, to string }{
		"replicas":      {`"replicas": 6`, `"replicas": 2`},
		"a probe":       {`"periodSeconds": 10`, `"periodSeconds": 60`},
		"a resource":    {`"memory": "64Mi"`, `"memory": "512Mi"`},
		"the analysis":  {`"templateName": "success-rate"`, `"templateName": "something-else"`},
		"the router":    {`"stableIngress": "payments-api"`, `"stableIngress": "other"`},
		"a Service":     {`"canaryService": "payments-api-canary"`, `"canaryService": "other"`},
		"an env value":  {`"value": "info"`, `"value": "debug"`},
		"the sidecar":   {`"name": "logging-sidecar"`, `"name": "other-sidecar"`},
		"a pull secret": {`"name": "ghcr-pull"`, `"name": "other-pull"`},
	} {
		t.Run(name, func(t *testing.T) {
			// The change is made to the document the patch is applied to, so
			// `after` is a real Rollout that differs from `before` in one more
			// place than the patch named.
			drifted := strings.Replace(richRollout, mutation.from, mutation.to, 1)
			if drifted == richRollout {
				t.Fatalf("the fixture does not contain %q", mutation.from)
			}
			err := patch.Verify([]byte(richRollout), apply(t, drifted, patch))
			assertRejection(t, err, "patch_changed_more_than_it_said")
		})
	}
}

// resourceVersion, generation and status move on every write and are not
// SafeLane's doing.
func TestVerifyIgnoresWhatKubernetesMovesOnAnyWrite(t *testing.T) {
	patch := build(t)
	after := apply(t, richRollout, patch)
	bumped := strings.NewReplacer(
		`"resourceVersion":"84213"`, `"resourceVersion":"84999"`,
		`"generation":7`, `"generation":8`,
		`"currentStepIndex":3`, `"currentStepIndex":0`,
	).Replace(string(after))
	if bumped == string(after) {
		t.Fatal("nothing was bumped; the fixture shape changed")
	}

	if err := patch.Verify([]byte(richRollout), []byte(bumped)); err != nil {
		t.Errorf("Verify complained about Kubernetes bookkeeping: %v", err)
	}
}

// apply is a small RFC 6902 applier: enough to run the four operations this
// package produces, and no more. It refuses a failed test the way an API
// server would.
func apply(t *testing.T, document string, patch releasepatch.Patch) []byte {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal([]byte(document), &object); err != nil {
		t.Fatal(err)
	}
	for _, operation := range patch.Operations {
		var value any
		if len(operation.Value) > 0 {
			if err := json.Unmarshal(operation.Value, &value); err != nil {
				t.Fatal(err)
			}
		}
		switch operation.Op {
		case "test":
			if got := read(t, object, operation.Path); got != value {
				t.Fatalf("test %s failed: %v != %v", operation.Path, got, value)
			}
		case "replace":
			write(t, object, operation.Path, value)
		default:
			t.Fatalf("unexpected operation %q", operation.Op)
		}
	}
	raw, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func walk(t *testing.T, object map[string]any, path string) (any, string) {
	t.Helper()
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	var current any = object
	for _, part := range parts[:len(parts)-1] {
		switch node := current.(type) {
		case map[string]any:
			current = node[part]
		case []any:
			index := 0
			if _, err := fmtSscan(part, &index); err != nil {
				t.Fatalf("bad index %q in %s", part, path)
			}
			current = node[index]
		default:
			t.Fatalf("cannot walk %s", path)
		}
	}
	return current, parts[len(parts)-1]
}

func read(t *testing.T, object map[string]any, path string) any {
	t.Helper()
	parent, key := walk(t, object, path)
	switch node := parent.(type) {
	case map[string]any:
		return node[key]
	case []any:
		index := 0
		if _, err := fmtSscan(key, &index); err != nil {
			t.Fatal(err)
		}
		return node[index]
	}
	return nil
}

func write(t *testing.T, object map[string]any, path string, value any) {
	t.Helper()
	parent, key := walk(t, object, path)
	switch node := parent.(type) {
	case map[string]any:
		node[key] = value
	case []any:
		index := 0
		if _, err := fmtSscan(key, &index); err != nil {
			t.Fatal(err)
		}
		node[index] = value
	default:
		t.Fatalf("cannot write %s", path)
	}
}

func fmtSscan(s string, out *int) (int, error) {
	value := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errNotANumber
		}
		value = value*10 + int(c-'0')
	}
	*out = value
	return 1, nil
}

var errNotANumber = release.Internal("not_a_number", "not a number")

func assertRejection(t *testing.T, err error, code string) {
	t.Helper()
	for _, e := range release.Flatten(err) {
		if e.Code == code {
			return
		}
	}
	t.Errorf("want a rejection with code %q, got %v", code, err)
}

func at(minute int) time.Time {
	return time.Date(2026, 8, 21, 12, minute, 0, 0, time.UTC)
}
