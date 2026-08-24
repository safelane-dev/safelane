package discovery_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/AndrewMaged814/safelane/internal/config"
	"github.com/AndrewMaged814/safelane/internal/discovery"
)

func selectionFor(t *testing.T, svc discovery.Service) discovery.Selection {
	t.Helper()
	target, err := svc.Inspect(context.Background(), ".", "safelane-demo-api", "safelane-demo-api")
	if err != nil {
		t.Fatal(err)
	}
	return discovery.Selection{
		Application: "safelane-demo-api",
		Environment: "production",
		Impact:      config.ImpactCritical,
		Context:     target.Context,
		Namespace:   "safelane-demo-api",
		Rollout:     "safelane-demo-api",
		Container:   "api",
		Fingerprint: target.Fingerprint,
	}
}

// A registration test starts from fake Git and Kubernetes observations and
// asserts the written file.
func TestRegisterWritesTheConfigurationItObserved(t *testing.T) {
	home := t.TempDir()
	c := demoCluster(t)
	svc := serviceWith(c)

	got, err := svc.Register(context.Background(), ".", home, selectionFor(t, svc))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !got.Changed {
		t.Error("the first registration reported no change")
	}

	written, err := os.ReadFile(got.Path)
	if err != nil {
		t.Fatal(err)
	}
	// The preview is the written bytes. A preview that could disagree with the
	// file is not a preview.
	if string(written) != string(got.File) {
		t.Errorf("the previewed file and the written file differ:\n--- preview ---\n%s\n--- written ---\n%s", got.File, written)
	}

	cfg, err := config.Parse(written)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Application.Repository != "andrewmaged814/safelane-demo-api" {
		t.Errorf("repository = %q", cfg.Application.Repository)
	}
	// The image repository is the live image with its reference stripped: a
	// digest is one build, and configuration names the repository.
	if cfg.Artifact.Image != "ghcr.io/andrewmaged814/safelane-demo-api" {
		t.Errorf("image = %q", cfg.Artifact.Image)
	}
	if cfg.Artifact.Container != "api" {
		t.Errorf("container = %q", cfg.Artifact.Container)
	}
	env, ok := cfg.Environment("production")
	if !ok {
		t.Fatalf("environments = %v", cfg.EnvironmentNames())
	}
	if env.Kubernetes.Context != "safelane-caller-safelane-demo-api" || env.Kubernetes.Namespace != "safelane-demo-api" {
		t.Errorf("kubernetes = %+v", env.Kubernetes)
	}
	if env.Impact != config.ImpactCritical {
		t.Errorf("impact = %q", env.Impact)
	}

	// Three lanes, compiled, not asked about.
	if names := cfg.ReleaseSettings.LaneNames(); strings.Join(names, ",") != "fast,guarded,standard" {
		t.Errorf("lanes = %v", names)
	}
	assertOnlyReads(t, c)
}

func TestRegisterTwiceWithNothingNewWritesNothing(t *testing.T) {
	home := t.TempDir()
	svc := serviceWith(demoCluster(t))
	selection := selectionFor(t, svc)

	if _, err := svc.Register(context.Background(), ".", home, selection); err != nil {
		t.Fatal(err)
	}
	again, err := svc.Register(context.Background(), ".", home, selection)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if again.Changed {
		t.Error("an unchanged registration reported a change")
	}
}

// Registration re-reads. Registering what a person saw and registering
// whatever happens to be there now are different things.
func TestRegisterRefusesWhenTheNamespaceMovedUnderIt(t *testing.T) {
	home := t.TempDir()
	c := demoCluster(t)
	svc := serviceWith(c)
	selection := selectionFor(t, svc)

	moved := strings.Replace(fixture(t, "safelane-demo-api"), `"name": "api"`, `"name": "server"`, 1)
	c.responses["get rollouts.argoproj.io safelane-demo-api -n safelane-demo-api -o json"] = moved

	_, err := svc.Register(context.Background(), ".", home, selection)
	assertRejection(t, err, "stale_discovery")
	if _, statErr := os.Stat(config.ForApp(home, "safelane-demo-api").File); !os.IsNotExist(statErr) {
		t.Error("a refused registration still wrote a file")
	}
}

func TestRegisterRefusesAnUnsupportedRollout(t *testing.T) {
	home := t.TempDir()
	c := demoCluster(t)
	svc := serviceWith(c)
	delete(c.responses, "get analysistemplate success-rate -o json -n safelane-demo-api")

	target, err := svc.Inspect(context.Background(), ".", "safelane-demo-api", "safelane-demo-api")
	if err != nil {
		t.Fatal(err)
	}
	selection := selectionFor(t, svc)
	selection.Fingerprint = target.Fingerprint

	_, err = svc.Register(context.Background(), ".", home, selection)
	assertRejection(t, err, "analysis_template_not_resolved")
}

func TestRegisterRefusesAContainerThatIsNotThere(t *testing.T) {
	home := t.TempDir()
	svc := serviceWith(demoCluster(t))
	selection := selectionFor(t, svc)
	selection.Container = "sidecar"

	_, err := svc.Register(context.Background(), ".", home, selection)
	assertRejection(t, err, "unknown_container")
	if err == nil || !strings.Contains(err.Error(), "api") {
		t.Errorf("the refusal should name the containers that do exist: %v", err)
	}
}

func TestRegisterRefusesAnIncompleteSelection(t *testing.T) {
	home := t.TempDir()
	svc := serviceWith(demoCluster(t))
	selection := selectionFor(t, svc)
	selection.Environment = ""

	_, err := svc.Register(context.Background(), ".", home, selection)
	assertRejection(t, err, "missing_selection_field")
}

func TestRegisterRefusesAnUnknownImpact(t *testing.T) {
	home := t.TempDir()
	svc := serviceWith(demoCluster(t))
	selection := selectionFor(t, svc)
	selection.Impact = "extreme"

	_, err := svc.Register(context.Background(), ".", home, selection)
	assertRejection(t, err, "invalid_impact")
}

// A second environment is added, not substituted, and the operator's policy
// block survives.
func TestRegisteringASecondEnvironmentPreservesTheFirstAndThePolicy(t *testing.T) {
	home := t.TempDir()
	c := demoCluster(t)
	svc := serviceWith(c)
	if _, err := svc.Register(context.Background(), ".", home, selectionFor(t, svc)); err != nil {
		t.Fatal(err)
	}

	path := config.ForApp(home, "safelane-demo-api").File
	edited := strings.Replace(mustRead(t, path), "weights: [50, 100]", "weights: [10, 100] # we ship slowly", 1)
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}

	staging := selectionFor(t, svc)
	staging.Environment = "staging"
	staging.Impact = config.ImpactLow
	if _, err := svc.Register(context.Background(), ".", home, staging); err != nil {
		t.Fatalf("Register: %v", err)
	}

	after := mustRead(t, path)
	if !strings.Contains(after, "weights: [10, 100] # we ship slowly") {
		t.Errorf("the operator's edited lane did not survive:\n%s", after)
	}
	cfg, err := config.Parse([]byte(after))
	if err != nil {
		t.Fatal(err)
	}
	if names := cfg.EnvironmentNames(); strings.Join(names, ",") != "production,staging" {
		t.Errorf("environments = %v", names)
	}
}

func TestRegistrationNamesTheAnalysisItWillWatch(t *testing.T) {
	home := t.TempDir()
	svc := serviceWith(demoCluster(t))
	got, err := svc.Register(context.Background(), ".", home, selectionFor(t, svc))
	if err != nil {
		t.Fatal(err)
	}
	name, provider := got.Analysis()
	if name != "success-rate" || provider != "Prometheus" {
		t.Errorf("analysis = %q checked by %q", name, provider)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
