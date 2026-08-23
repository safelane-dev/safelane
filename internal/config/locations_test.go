package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AndrewMaged814/safelane/internal/config"
)

// All four locations come from the Application and Environment names and
// nothing else. In particular the controller kubeconfig is derived rather than
// configured: a path in a settings file is a path somebody can repoint.
func TestAllFourPathsDeriveFromTwoNames(t *testing.T) {
	home := filepath.FromSlash("/safelane-home")
	app := config.ForApp(home, "payments-api")
	env := app.ForEnvironment("production")

	want := map[string]string{
		"safelane.yml":          "/safelane-home/apps/payments-api/safelane.yml",
		"controller kubeconfig": "/safelane-home/apps/payments-api/environments/production/identities/controller/kubeconfig",
		"releases":              "/safelane-home/apps/payments-api/environments/production/releases",
		"history":               "/safelane-home/apps/payments-api/environments/production/history.jsonl",
	}
	got := map[string]string{
		"safelane.yml":          app.File,
		"controller kubeconfig": env.ControllerKubeconfig,
		"releases":              env.ReleasesDir,
		"history":               env.HistoryFile,
	}
	for name, wantPath := range want {
		if filepath.ToSlash(got[name]) != wantPath {
			t.Errorf("%s = %s, want %s", name, filepath.ToSlash(got[name]), wantPath)
		}
	}
}

func TestEnvironmentsAreIndependentOfEachOther(t *testing.T) {
	app := config.ForApp(filepath.FromSlash("/home"), "payments-api")
	if app.ForEnvironment("production").Dir == app.ForEnvironment("staging").Dir {
		t.Error("two environments resolved to the same directory")
	}
}

func TestHomeFollowsTheEnvironmentVariable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.HomeEnv, dir)
	got, err := config.Home()
	if err != nil {
		t.Fatalf("Home: %v", err)
	}
	if got != dir {
		t.Errorf("Home = %s, want %s", got, dir)
	}
}

// A directory left behind by an earlier version is not an Application. It is
// skipped rather than half-read, and it is left exactly where it is.
func TestAppsSkipsDirectoriesWithNoConfiguration(t *testing.T) {
	home := t.TempDir()
	writeFile(t, config.ForApp(home, "payments-api").File, golden)
	legacy := filepath.Join(home, "apps", "orders-api", "project.yml")
	writeFile(t, legacy, "version: 4\n")

	names, err := config.Apps(home)
	if err != nil {
		t.Fatalf("Apps: %v", err)
	}
	if len(names) != 1 || names[0] != "payments-api" {
		t.Errorf("Apps = %v, want [payments-api]", names)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Errorf("the earlier version's file was removed: %v", err)
	}
}

// Configuration written by an earlier version is ignored, named so a person can
// see it is still there, and never deleted.
func TestLoadIgnoresAnEarlierVersionAndDeletesNothing(t *testing.T) {
	home := t.TempDir()
	app := config.ForApp(home, "payments-api")
	project := filepath.Join(app.AppDir, "project.yml")
	policy := filepath.Join(app.AppDir, "policy.yml")
	writeFile(t, project, "version: 4\napplication: payments-api\n")
	writeFile(t, policy, "version: 2\nlanes: {}\n")

	_, err := config.Load(app.File)
	assertCode(t, err, "unregistered_application")
	assertRemedy(t, err, "Register this application again")
	assertMentions(t, err, "project.yml")
	assertMentions(t, err, "policy.yml")

	for _, path := range []string{project, policy} {
		if _, statErr := os.Stat(path); statErr != nil {
			t.Errorf("%s was deleted: %v", filepath.Base(path), statErr)
		}
	}
}

func TestLoadReadsAWrittenFile(t *testing.T) {
	home := t.TempDir()
	app := config.ForApp(home, "payments-api")
	if _, err := config.Write(app.File, []byte(golden)); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(app.File)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Application.Name != "payments-api" {
		t.Errorf("application = %q", cfg.Application.Name)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, ".yml") {
		t.Fatalf("writeFile is for YAML fixtures, got %s", path)
	}
}
