package discovery_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AndrewMaged814/safelane/internal/discovery"
	"github.com/AndrewMaged814/safelane/internal/release"
)

// cluster is a fake kubectl: a map from the exact argument line to what that
// read returns. Anything not in the map is "not found", which is what a real
// cluster says too.
type cluster struct {
	responses map[string]string
	issued    []string
}

func (c *cluster) run(_ context.Context, args []string) ([]byte, error) {
	line := strings.Join(args, " ")
	c.issued = append(c.issued, line)
	if out, ok := c.responses[line]; ok {
		return []byte(out), nil
	}
	return nil, fmt.Errorf("Error from server (NotFound): %s", line)
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func list(items ...string) string {
	return `{"apiVersion":"v1","kind":"List","items":[` + strings.Join(items, ",") + `]}`
}

func service(name string) string {
	return `{"apiVersion":"v1","kind":"Service","metadata":{"name":"` + name + `"}}`
}

// demoCluster is the cluster ticket 01's package installs.
func demoCluster(t *testing.T) *cluster {
	t.Helper()
	return &cluster{responses: map[string]string{
		"config current-context":                                                  "safelane-caller-safelane-demo-api\n",
		"get rollouts.argoproj.io -n safelane-demo-api -o json":                   list(fixture(t, "safelane-demo-api")),
		"get rollouts.argoproj.io safelane-demo-api -n safelane-demo-api -o json": fixture(t, "safelane-demo-api"),
		"get service safelane-demo-api-stable -n safelane-demo-api -o json":       service("safelane-demo-api-stable"),
		"get service safelane-demo-api-canary -n safelane-demo-api -o json":       service("safelane-demo-api-canary"),
		"get analysistemplate success-rate -o json -n safelane-demo-api":          fixture(t, "success-rate"),
	}}
}

func serviceWith(c *cluster) discovery.Service {
	return discovery.Service{
		Run:    c.run,
		Origin: func(string) (string, error) { return "andrewmaged814/safelane-demo-api", nil },
	}
}

func TestDiscoverListsRolloutsWithTheirContainers(t *testing.T) {
	c := demoCluster(t)
	found, err := serviceWith(c).Discover(context.Background(), ".", "safelane-demo-api")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	if len(found.Rollouts) != 1 {
		t.Fatalf("rollouts = %d, want 1", len(found.Rollouts))
	}
	rollout := found.Rollouts[0]
	if rollout.Name != "safelane-demo-api" {
		t.Errorf("name = %q", rollout.Name)
	}
	if len(rollout.Containers) != 1 || rollout.Containers[0].Name != "api" {
		t.Errorf("containers = %+v", rollout.Containers)
	}
	if !strings.HasPrefix(rollout.Containers[0].Image, "ghcr.io/") {
		t.Errorf("image = %q", rollout.Containers[0].Image)
	}
	if !rollout.Environment.Supported {
		t.Errorf("unsupported: %+v", rollout.Environment.Reasons)
	}
	if rollout.Fingerprint == "" {
		t.Error("discovery did not return the fingerprint registration requires")
	}
	if len(rollout.Analysis) != 1 || rollout.Analysis[0].Name != "success-rate" {
		t.Errorf("discovery did not return the selected rollout's health analysis: %+v", rollout.Analysis)
	}
	if !rollout.Artifact.Supported {
		t.Errorf("artifact unsupported: %+v", rollout.Artifact.Reasons)
	}
	if len(found.RegistrationCandidates) != 1 {
		t.Fatalf("registration candidates = %+v", found.RegistrationCandidates)
	}
	candidate := found.RegistrationCandidates[0]
	if candidate.Application != "safelane-demo-api" || candidate.Container != "api" ||
		candidate.Namespace != "safelane-demo-api" || candidate.Rollout != "safelane-demo-api" ||
		candidate.Context != found.Context || candidate.Fingerprint != rollout.Fingerprint {
		t.Errorf("registration candidate = %+v", candidate)
	}
	if candidate.Environment != "" || candidate.Impact != "" {
		t.Errorf("user answers were invented: %+v", candidate)
	}
}

// The context is reported so a person can see which cluster answered. Nothing
// here selects one.
func TestDiscoverReportsTheContextAndNeverChangesIt(t *testing.T) {
	c := demoCluster(t)
	found, err := serviceWith(c).Discover(context.Background(), ".", "safelane-demo-api")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if found.Context != "safelane-caller-safelane-demo-api" {
		t.Errorf("context = %q", found.Context)
	}
	assertOnlyReads(t, c)
}

func TestInspectReadsServicesAndAnalysis(t *testing.T) {
	c := demoCluster(t)
	target, err := serviceWith(c).Inspect(context.Background(), ".", "safelane-demo-api", "safelane-demo-api")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}

	if target.StableService != "safelane-demo-api-stable" || target.CanaryService != "safelane-demo-api-canary" {
		t.Errorf("services = %q / %q", target.StableService, target.CanaryService)
	}
	if len(target.Analysis) != 1 {
		t.Fatalf("analysis = %+v", target.Analysis)
	}
	if got := target.Analysis[0]; got.Name != "success-rate" || !got.Resolved || got.Provider != "Prometheus" {
		t.Errorf("analysis = %+v", got)
	}
	if target.Analysis[0].DefinitionDigest == "" {
		t.Error("analysis definition was not content-addressed")
	}
	if !target.Environment.Supported {
		t.Errorf("environment unsupported: %+v", target.Environment.Reasons)
	}
	if !target.Artifact.Supported {
		t.Errorf("artifact unsupported: %+v", target.Artifact.Reasons)
	}
	assertOnlyReads(t, c)
}

// The two questions are answered apart. The official Argo Istio example passes
// the Kubernetes half and fails the Artifact half, and being told only
// "incompatible" would hide which.
func TestOfficialArgoFixturePassesEnvironmentAndFailsArtifact(t *testing.T) {
	c := &cluster{responses: map[string]string{
		"config current-context":                                           "istio-demo",
		"get rollouts.argoproj.io -n istio-rollout -o json":                list(fixture(t, "argo-istio-rollout")),
		"get rollouts.argoproj.io istio-rollout -n istio-rollout -o json":  fixture(t, "argo-istio-rollout"),
		"get service istio-rollout-stable -n istio-rollout -o json":        service("istio-rollout-stable"),
		"get service istio-rollout-canary -n istio-rollout -o json":        service("istio-rollout-canary"),
		"get analysistemplate istio-success-rate -o json -n istio-rollout": fixture(t, "istio-success-rate"),
	}}
	svc := discovery.Service{Run: c.run, Origin: func(string) (string, error) { return "argoproj/rollouts-demo", nil }}

	target, err := svc.Inspect(context.Background(), ".", "istio-rollout", "istio-rollout")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}

	if !target.Environment.Supported {
		t.Errorf("environment should be supported, got %+v", target.Environment.Reasons)
	}
	if target.Artifact.Supported {
		t.Error("artifact should not be supported: the image is a mutable Docker Hub tag with no registry provenance path")
	}
	if len(target.Analysis) != 1 || target.Analysis[0].Provider != "Prometheus" {
		t.Errorf("analysis = %+v", target.Analysis)
	}
	if target.TrafficRouter != "istio" {
		t.Errorf("traffic router = %q, want istio", target.TrafficRouter)
	}
	// Istio traffic routing and timed pauses are shapes SafeLane leaves alone,
	// not shapes it refuses.
	for _, reason := range target.Environment.Reasons {
		t.Errorf("unexpected reason: %s", reason.Code)
	}
}

func TestInspectPreservesEveryAnalysisMetric(t *testing.T) {
	c := demoCluster(t)
	c.responses["get analysistemplate success-rate -o json -n safelane-demo-api"] = `{"metadata":{"name":"success-rate"},"spec":{"metrics":[` +
		`{"name":"success","interval":"30s","successCondition":"result[0] >= 0.99","provider":{"prometheus":{}}},` +
		`{"name":"latency","interval":"1m","successCondition":"result[0] < 0.5","provider":{"prometheus":{}}}]}}`
	target, err := serviceWith(c).Inspect(context.Background(), ".", "safelane-demo-api", "safelane-demo-api")
	if err != nil {
		t.Fatal(err)
	}
	if len(target.Analysis) != 1 || len(target.Analysis[0].Metrics) != 2 {
		t.Fatalf("analysis metrics = %+v", target.Analysis)
	}
	if target.Analysis[0].Metrics[1].Name != "latency" || target.Analysis[0].Metrics[1].Condition != "result[0] < 0.5" {
		t.Fatalf("second metric = %+v", target.Analysis[0].Metrics[1])
	}
}

func TestAnalysisBodyPreservesUnknownFieldsAndRedactsCredentials(t *testing.T) {
	c := demoCluster(t)
	c.responses["get analysistemplate success-rate -o json -n safelane-demo-api"] = `{
      "metadata":{"name":"success-rate"},
      "spec":{
		"args":[{"name":"service","value":"candidate"},{"name":"api-token","value":"named-argument-secret"}],
        "dryRun":[{"metricName":"shadow"}],
		"metrics":[{"name":"success","successCondition":"result[0] >= 0.99","provider":{"web":{"url":"https://person:password@example.test/check?token=query-secret&region=eu","endpoints":["https://array-user:array-password@example.test"],"headers":{"Authorization":"Bearer never-store-this"}}}}]
      }
    }`
	target, err := serviceWith(c).Inspect(context.Background(), ".", "safelane-demo-api", "safelane-demo-api")
	if err != nil {
		t.Fatal(err)
	}
	body := string(target.Analysis[0].Body)
	for _, expected := range []string{`"args"`, `"dryRun"`, `"successCondition"`, `"[omitted]"`} {
		if !strings.Contains(body, expected) {
			t.Errorf("analysis body is missing %s:\n%s", expected, body)
		}
	}
	for _, secret := range []string{"never-store-this", "named-argument-secret", "password", "query-secret", "array-user"} {
		if strings.Contains(body, secret) {
			t.Fatalf("analysis body retained %q:\n%s", secret, body)
		}
	}
	for _, omitted := range []string{`"value": "[omitted]"`, `"url": "[omitted]"`, `"headers"`} {
		if !strings.Contains(body, omitted) {
			t.Fatalf("analysis body did not retain a safe structural marker %s:\n%s", omitted, body)
		}
	}
}

func TestUnsupportedShapesAreNamedAndExplained(t *testing.T) {
	for name, tc := range map[string]struct {
		rollout string
		code    string
		mention string
	}{
		"blue-green": {
			rollout: `{"metadata":{"name":"r"},"spec":{"strategy":{"blueGreen":{"activeService":"a"}},"template":{"spec":{"containers":[{"name":"c","image":"ghcr.io/o/i:v1"}]}}}}`,
			code:    "blue_green_strategy",
			mention: "blue-green",
		},
		"workloadRef": {
			rollout: `{"metadata":{"name":"r"},"spec":{"workloadRef":{"kind":"Deployment","name":"payments"},"strategy":{"canary":{"analysis":{"templates":[{"templateName":"t"}]}}}}}`,
			code:    "workload_reference",
			mention: "payments",
		},
		"no inline container": {
			rollout: `{"metadata":{"name":"r"},"spec":{"strategy":{"canary":{"analysis":{"templates":[{"templateName":"t"}]}}},"template":{"spec":{"containers":[]}}}}`,
			code:    "no_inline_container",
			mention: "no container",
		},
		"no background analysis": {
			rollout: `{"metadata":{"name":"r"},"spec":{"strategy":{"canary":{"steps":[{"setWeight":50},{"analysis":{"templates":[{"templateName":"t"}]}}]}},"template":{"spec":{"containers":[{"name":"c","image":"ghcr.io/o/i:v1"}]}}}}`,
			code:    "no_background_analysis",
			mention: "background analysis",
		},
		"unsupported step": {
			rollout: `{"metadata":{"name":"r"},"spec":{"strategy":{"canary":{"analysis":{"templates":[{"templateName":"t"}]},"steps":[{"setWeight":50},{"experiment":{"templates":[]}}]}},"template":{"spec":{"containers":[{"name":"c","image":"ghcr.io/o/i:v1"}]}}}}`,
			code:    "unsupported_step",
			mention: "experiment",
		},
		"argo cd owned": {
			rollout: `{"metadata":{"name":"r","labels":{"argocd.argoproj.io/instance":"payments"}},"spec":{"strategy":{"canary":{"analysis":{"templates":[{"templateName":"t"}]}}},"template":{"spec":{"containers":[{"name":"c","image":"ghcr.io/o/i:v1"}]}}}}`,
			code:    "managed_by_gitops",
			mention: "Argo CD",
		},
		"flux owned": {
			rollout: `{"metadata":{"name":"r","labels":{"kustomize.toolkit.fluxcd.io/name":"apps"}},"spec":{"strategy":{"canary":{"analysis":{"templates":[{"templateName":"t"}]}}},"template":{"spec":{"containers":[{"name":"c","image":"ghcr.io/o/i:v1"}]}}}}`,
			code:    "managed_by_gitops",
			mention: "Flux",
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := &cluster{responses: map[string]string{
				"config current-context":                   "some-context",
				"get rollouts.argoproj.io -n apps -o json": list(tc.rollout),
			}}
			svc := discovery.Service{Run: c.run, Origin: func(string) (string, error) { return "acme/payments", nil }}
			found, err := svc.Discover(context.Background(), ".", "apps")
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			compat := found.Rollouts[0].Environment
			if compat.Supported {
				t.Fatal("this shape should not be supported")
			}
			assertReason(t, compat, tc.code, tc.mention)
		})
	}
}

func TestUnresolvedServiceAndTemplateAreExplained(t *testing.T) {
	c := demoCluster(t)
	delete(c.responses, "get service safelane-demo-api-canary -n safelane-demo-api -o json")
	delete(c.responses, "get analysistemplate success-rate -o json -n safelane-demo-api")

	target, err := serviceWith(c).Inspect(context.Background(), ".", "safelane-demo-api", "safelane-demo-api")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if target.Environment.Supported {
		t.Fatal("a Rollout pointing at things that are not there is not releasable")
	}
	assertReason(t, target.Environment, "service_not_resolved", "safelane-demo-api-canary")
	assertReason(t, target.Environment, "analysis_template_not_resolved", "success-rate")
	if target.Analysis[0].Resolved {
		t.Error("an unreadable template was reported as resolved")
	}
}

func TestZeroOneAndManyRollouts(t *testing.T) {
	one := fixture(t, "safelane-demo-api")
	other := strings.Replace(one, `"name": "safelane-demo-api",`, `"name": "aaa-other",`, 1)

	for name, tc := range map[string]struct {
		items []string
		want  []string
	}{
		"zero": {items: nil, want: nil},
		"one":  {items: []string{one}, want: []string{"safelane-demo-api"}},
		"many": {items: []string{one, other}, want: []string{"aaa-other", "safelane-demo-api"}},
	} {
		t.Run(name, func(t *testing.T) {
			c := &cluster{responses: map[string]string{
				"config current-context":                                "ctx",
				"get rollouts.argoproj.io -n safelane-demo-api -o json": list(tc.items...),
			}}
			found, err := serviceWith(c).Discover(context.Background(), ".", "safelane-demo-api")
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			var names []string
			for _, rollout := range found.Rollouts {
				names = append(names, rollout.Name)
			}
			if strings.Join(names, ",") != strings.Join(tc.want, ",") {
				t.Errorf("rollouts = %v, want %v", names, tc.want)
			}
		})
	}
}

func TestZeroOneAndManyContainers(t *testing.T) {
	build := func(containers string) string {
		return `{"metadata":{"name":"r"},"spec":{"strategy":{"canary":{"analysis":{"templates":[{"templateName":"t"}]}}},"template":{"spec":{"containers":[` + containers + `]}}}}`
	}
	for name, tc := range map[string]struct {
		containers string
		want       int
		supported  bool
	}{
		"zero": {containers: "", want: 0, supported: false},
		"one":  {containers: `{"name":"api","image":"ghcr.io/o/i:v1"}`, want: 1, supported: true},
		"many": {containers: `{"name":"api","image":"ghcr.io/o/i:v1"},{"name":"sidecar","image":"ghcr.io/o/s:v1"}`, want: 2, supported: true},
	} {
		t.Run(name, func(t *testing.T) {
			rolloutDoc := build(tc.containers)
			c := &cluster{responses: map[string]string{
				"config current-context":                     "ctx",
				"get rollouts.argoproj.io -n apps -o json":   list(rolloutDoc),
				"get rollouts.argoproj.io r -n apps -o json": rolloutDoc,
				"get analysistemplate t -o json -n apps":     `{"metadata":{"name":"t"},"spec":{"metrics":[{"name":"ok","provider":{"prometheus":{"query":"up"}}}]}}`,
			}}
			found, err := serviceWith(c).Discover(context.Background(), ".", "apps")
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			rollout := found.Rollouts[0]
			if len(rollout.Containers) != tc.want {
				t.Errorf("containers = %d, want %d", len(rollout.Containers), tc.want)
			}
			if rollout.Environment.Supported != tc.supported {
				t.Errorf("supported = %t, want %t (%+v)", rollout.Environment.Supported, tc.supported, rollout.Environment.Reasons)
			}
		})
	}
}

func TestImageRepositoryStripsTheReference(t *testing.T) {
	for image, want := range map[string]string{
		"ghcr.io/acme/payments-api:v1.2.3":     "ghcr.io/acme/payments-api",
		"ghcr.io/acme/payments-api@sha256:aaa": "ghcr.io/acme/payments-api",
		"ghcr.io/acme/payments-api":            "ghcr.io/acme/payments-api",
		"localhost:5000/acme/api:v1":           "localhost:5000/acme/api",
		"localhost:5000/acme/api":              "localhost:5000/acme/api",
	} {
		if got := discovery.ImageRepository(image); got != want {
			t.Errorf("ImageRepository(%q) = %q, want %q", image, got, want)
		}
	}
}

func TestDiscoverNeedsANamespace(t *testing.T) {
	c := demoCluster(t)
	if _, err := serviceWith(c).Discover(context.Background(), ".", "  "); err == nil {
		t.Fatal("Discover accepted an empty namespace")
	}
}

// Discovery reads one namespace. It never lists namespaces and never scans a
// cluster, because "SafeLane looked around your cluster" is not a thing anybody
// asked for.
func TestDiscoveryReadsOneNamespaceOnly(t *testing.T) {
	c := demoCluster(t)
	if _, err := serviceWith(c).Inspect(context.Background(), ".", "safelane-demo-api", "safelane-demo-api"); err != nil {
		t.Fatal(err)
	}
	for _, line := range c.issued {
		if strings.Contains(line, "--all-namespaces") || strings.Contains(line, "-A ") {
			t.Errorf("discovery scanned the cluster: %s", line)
		}
	}
}

func assertOnlyReads(t *testing.T, c *cluster) {
	t.Helper()
	forbidden := []string{"use-context", "apply", "patch", "create", "delete", "edit", "replace", "set-context"}
	for _, line := range c.issued {
		for _, verb := range forbidden {
			if strings.Contains(line, verb) {
				t.Errorf("discovery issued a write: kubectl %s", line)
			}
		}
	}
}

func assertReason(t *testing.T, compat discovery.Compatibility, code, mention string) {
	t.Helper()
	for _, reason := range compat.Reasons {
		if reason.Code != code {
			continue
		}
		if !strings.Contains(reason.Explanation, mention) {
			t.Errorf("reason %q says %q, want it to mention %q", code, reason.Explanation, mention)
		}
		if !strings.HasSuffix(strings.TrimSpace(reason.Explanation), ".") {
			t.Errorf("reason %q is not a sentence: %q", code, reason.Explanation)
		}
		return
	}
	t.Errorf("no reason with code %q; got %s", code, reasonCodes(compat))
}

func reasonCodes(compat discovery.Compatibility) string {
	codes := make([]string, 0, len(compat.Reasons))
	for _, reason := range compat.Reasons {
		codes = append(codes, reason.Code)
	}
	return strings.Join(codes, ", ")
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
