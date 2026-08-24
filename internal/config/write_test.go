package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AndrewMaged814/safelane/internal/config"
)

func discovered() config.Discovered {
	return config.Discovered{
		Application: config.Application{Name: "payments-api", Repository: "acme/payments-api"},
		Artifact:    config.Artifact{Container: "payments-api", Image: "ghcr.io/acme/payments-api"},
		Environment: config.Environment{
			Name:   "production",
			Impact: config.ImpactCritical,
			Kubernetes: config.Kubernetes{
				Context:   "safelane-caller-payments-api",
				Namespace: "payments",
				Rollout:   "payments-api",
			},
		},
	}
}

// The file a first registration writes is the file the plan documents, to the
// byte. This is the output contract for configuration.
func TestRenderProducesTheDocumentedFile(t *testing.T) {
	got := string(config.Render(discovered(), config.DefaultReleaseSettings()))
	if got != golden {
		t.Errorf("Render produced:\n%s\nwant:\n%s", got, golden)
	}
}

func TestRenderedFileParsesBack(t *testing.T) {
	raw := config.Render(discovered(), config.DefaultReleaseSettings())
	if _, err := config.Parse(raw); err != nil {
		t.Fatalf("SafeLane cannot read what it just wrote: %v", err)
	}
}

// The saved release settings belong to the application. Reconciling brings them across exactly as
// it was written -- hand-edited weights, comments, spacing and all -- because
// discovery has no opinion about any of it.
func TestReconcilePreservesTheReleaseSettingsByteForByte(t *testing.T) {
	releaseSettings := `policy:
  # We ship payments slowly. Do not "simplify" this.
  default_lane: guarded
  risk_mapping:
    low:    standard
    medium: guarded
    high:   guarded
  lanes:
    standard:
      weights: [ 10, 40, 100 ]
    guarded:
      weights:
        - 5
        - 10
        - 25
        - 100
`
	existing := strings.Replace(golden, golden[strings.Index(golden, "policy:"):], releaseSettings, 1)

	next, err := config.Reconcile([]byte(existing), discovered())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := string(next)
	if !strings.Contains(got, releaseSettings) {
		t.Errorf("the release-settings block was rewritten.\ngot:\n%s\nwant it to contain:\n%s", got, releaseSettings)
	}
}

// Re-registering one Environment must not disturb the others.
func TestReconcileReplacesOneEnvironmentAndLeavesTheRest(t *testing.T) {
	existing := strings.Replace(golden, `environments:
  - name: production`, `environments:
  - name: staging
    impact: low
    kubernetes:
      context: safelane-caller-payments-api
      namespace: payments-staging
      rollout: payments-api
  - name: production`, 1)

	moved := discovered()
	moved.Environment.Kubernetes.Namespace = "payments-prod"

	next, err := config.Reconcile([]byte(existing), moved)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	cfg, err := config.Parse(next)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if got := cfg.EnvironmentNames(); len(got) != 2 || got[0] != "staging" || got[1] != "production" {
		t.Fatalf("environments = %v, want [staging production]", got)
	}
	staging, _ := cfg.Environment("staging")
	if staging.Kubernetes.Namespace != "payments-staging" || staging.Impact != config.ImpactLow {
		t.Errorf("staging changed: %+v", staging)
	}
	production, _ := cfg.Environment("production")
	if production.Kubernetes.Namespace != "payments-prod" {
		t.Errorf("production namespace = %q, want payments-prod", production.Kubernetes.Namespace)
	}
}

func TestReconcileAppendsANewEnvironment(t *testing.T) {
	staging := discovered()
	staging.Environment = config.Environment{
		Name:       "staging",
		Impact:     config.ImpactLow,
		Kubernetes: config.Kubernetes{Context: "c", Namespace: "payments-staging", Rollout: "payments-api"},
	}

	next, err := config.Reconcile([]byte(golden), staging)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	cfg, err := config.Parse(next)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := cfg.EnvironmentNames(); len(got) != 2 || got[1] != "staging" {
		t.Errorf("environments = %v, want production then staging", got)
	}
}

// Reconciling onto a file SafeLane cannot read would mean guessing which half
// to keep, so it refuses instead.
func TestReconcileRefusesAnUnreadableFile(t *testing.T) {
	if _, err := config.Reconcile([]byte("version: 4\nproject: {}\n"), discovered()); err == nil {
		t.Fatal("Reconcile accepted a file written by an earlier version")
	}
}

// Registering twice with nothing new to say touches nothing at all -- not the
// bytes, not the modification time, not a temporary file.
func TestWriteIsANoOpWhenNothingChanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "safelane.yml")

	changed, err := config.Write(path, []byte(golden))
	if err != nil || !changed {
		t.Fatalf("first Write: changed=%v err=%v", changed, err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	changed, err = config.Write(path, []byte(golden))
	if err != nil {
		t.Fatalf("second Write: %v", err)
	}
	if changed {
		t.Error("an unchanged registration reported a change")
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Error("an unchanged registration rewrote the file")
	}
	assertNoTemporaryFiles(t, dir)
}

func TestWriteReplacesTheFileAndLeavesNoTemporaries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "safelane.yml")
	if _, err := config.Write(path, []byte(golden)); err != nil {
		t.Fatal(err)
	}

	next, err := config.Reconcile([]byte(golden), func() config.Discovered {
		d := discovered()
		d.Environment.Impact = config.ImpactSignificant
		return d
	}())
	if err != nil {
		t.Fatal(err)
	}
	changed, err := config.Write(path, next)
	if err != nil || !changed {
		t.Fatalf("Write: changed=%v err=%v", changed, err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	production, _ := cfg.Environment("production")
	if production.Impact != config.ImpactSignificant {
		t.Errorf("impact = %q, want significant", production.Impact)
	}
	assertNoTemporaryFiles(t, dir)
}

func TestWriteCreatesTheApplicationDirectory(t *testing.T) {
	path := config.ForApp(t.TempDir(), "payments-api").File
	if _, err := config.Write(path, []byte(golden)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Stat: %v", err)
	}
}

func assertNoTemporaryFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "safelane.yml" {
			t.Errorf("left %s behind in %s", entry.Name(), dir)
		}
	}
}
