package config_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/AndrewMaged814/safelane/internal/config"
	"github.com/AndrewMaged814/safelane/internal/release"
)

// golden is the exact shape decision 2 fixes. Every parse test starts from it,
// so a test that breaks because the shape drifted breaks visibly here.
const golden = `application:
  name: payments-api
  repository: acme/payments-api

artifact:
  container: payments-api
  image: ghcr.io/acme/payments-api

environments:
  - name: production
    impact: critical
    kubernetes:
      context: safelane-caller-payments-api
      namespace: payments
      rollout: payments-api

policy:
  default_lane: guarded
  risk_mapping:
    low: fast
    medium: standard
    high: guarded
  lanes:
    fast:
      weights: [50, 100]
    standard:
      weights: [25, 50, 100]
    guarded:
      weights: [25, 50, 75, 100]
`

func TestParseReadsTheDocumentedShape(t *testing.T) {
	cfg, err := config.Parse([]byte(golden))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if cfg.Application.Name != "payments-api" || cfg.Application.Repository != "acme/payments-api" {
		t.Errorf("application = %+v", cfg.Application)
	}
	if cfg.Artifact.Container != "payments-api" || cfg.Artifact.Image != "ghcr.io/acme/payments-api" {
		t.Errorf("artifact = %+v", cfg.Artifact)
	}
	if len(cfg.Environments) != 1 {
		t.Fatalf("environments = %d, want 1", len(cfg.Environments))
	}
	env := cfg.Environments[0]
	if env.Name != "production" || env.Impact != config.ImpactCritical {
		t.Errorf("environment = %+v", env)
	}
	want := config.Kubernetes{Context: "safelane-caller-payments-api", Namespace: "payments", Rollout: "payments-api"}
	if env.Kubernetes != want {
		t.Errorf("kubernetes = %+v, want %+v", env.Kubernetes, want)
	}
	if cfg.Policy.DefaultLane != "guarded" {
		t.Errorf("default_lane = %q", cfg.Policy.DefaultLane)
	}
	if got := cfg.Policy.Lanes["standard"].Weights; len(got) != 3 || got[2] != 100 {
		t.Errorf("standard weights = %v", got)
	}
}

// The removed settings are not "ignored if present" -- they are unknown fields,
// so a file carrying one is refused rather than half-read.
func TestParseRejectsRemovedSettings(t *testing.T) {
	for _, removed := range []string{
		"version: 4",
		"controller_kubeconfig: ./identities/controller/kubeconfig",
		"default_branch: main",
		"required_check: build-and-push",
		"image_tag_pattern: sha-{sha}",
		"template_path: ./release-template",
		"assessment:\n  heuristic:\n    path_rules: []",
	} {
		t.Run(strings.SplitN(removed, ":", 2)[0], func(t *testing.T) {
			if _, err := config.Parse([]byte(removed + "\n" + golden)); err == nil {
				t.Fatalf("Parse accepted %q", removed)
			}
		})
	}
}

func TestParseRejectsDuplicateEnvironmentNames(t *testing.T) {
	raw := strings.Replace(golden, `environments:
  - name: production`, `environments:
  - name: production
    impact: low
    kubernetes:
      context: other
      namespace: other
      rollout: other
  - name: production`, 1)

	err := parseErr(t, raw)
	assertCode(t, err, "duplicate_environment")
	assertMentions(t, err, "production")
}

// A lane declared twice cannot survive into the decoded map, so the duplicate
// has to be caught while the document is still a node tree.
func TestParseRejectsDuplicateLaneNames(t *testing.T) {
	raw := strings.Replace(golden, `    fast:
      weights: [50, 100]`, `    fast:
      weights: [50, 100]
    fast:
      weights: [100]`, 1)

	err := parseErr(t, raw)
	assertCode(t, err, "duplicate_lane")
	assertMentions(t, err, "fast")
}

func TestLaneWeightsMustIncrease(t *testing.T) {
	raw := strings.Replace(golden, "weights: [25, 50, 100]", "weights: [50, 25, 100]", 1)
	err := parseErr(t, raw)
	assertCode(t, err, "weights_not_increasing")
}

func TestLaneWeightsMustNotRepeat(t *testing.T) {
	raw := strings.Replace(golden, "weights: [25, 50, 100]", "weights: [50, 50, 100]", 1)
	err := parseErr(t, raw)
	assertCode(t, err, "weights_not_increasing")
}

func TestLaneMustEndAtOneHundred(t *testing.T) {
	raw := strings.Replace(golden, "weights: [25, 50, 100]", "weights: [25, 50, 75]", 1)
	err := parseErr(t, raw)
	assertCode(t, err, "lane_does_not_finish")
	assertMentions(t, err, "standard")
}

func TestRiskMappingMustNameDeclaredLanes(t *testing.T) {
	raw := strings.Replace(golden, "medium: standard", "medium: careful", 1)
	err := parseErr(t, raw)
	assertCode(t, err, "undeclared_lane")
	assertMentions(t, err, "careful")
}

func TestEveryRiskLevelMustBeMapped(t *testing.T) {
	raw := strings.Replace(golden, "    medium: standard\n", "", 1)
	err := parseErr(t, raw)
	assertCode(t, err, "missing_config_field")
	assertMentions(t, err, "medium")
}

func TestDefaultLaneMustBeDeclared(t *testing.T) {
	raw := strings.Replace(golden, "default_lane: guarded", "default_lane: paranoid", 1)
	err := parseErr(t, raw)
	assertCode(t, err, "undeclared_lane")
}

func TestImpactMustBeOneOfThree(t *testing.T) {
	raw := strings.Replace(golden, "impact: critical", "impact: extreme", 1)
	err := parseErr(t, raw)
	assertCode(t, err, "invalid_impact")
}

// Application and Environment names become directory names, so a name that
// walks out of the layout is refused before it can be used as a path.
func TestNamesThatAreNotPathSegmentsAreRejected(t *testing.T) {
	for _, name := range []string{"../escape", "a/b", ".."} {
		t.Run(name, func(t *testing.T) {
			raw := strings.Replace(golden, "  name: payments-api", "  name: "+quote(name), 1)
			err := parseErr(t, raw)
			assertCode(t, err, "unsafe_name")
		})
	}
}

func TestArtifactImageIsARepositoryNotAReference(t *testing.T) {
	for name, image := range map[string]string{
		"tag":    "ghcr.io/acme/payments-api:v1",
		"digest": "ghcr.io/acme/payments-api@sha256:" + strings.Repeat("a", 64),
	} {
		t.Run(name, func(t *testing.T) {
			raw := strings.Replace(golden, "image: ghcr.io/acme/payments-api", "image: "+quote(image), 1)
			err := parseErr(t, raw)
			assertCode(t, err, "image_is_a_reference")
		})
	}
}

// A registry port is a colon that is not a tag.
func TestArtifactImageMayCarryARegistryPort(t *testing.T) {
	raw := strings.Replace(golden, "image: ghcr.io/acme/payments-api", `image: "localhost:5000/acme/payments-api"`, 1)
	if _, err := config.Parse([]byte(raw)); err != nil {
		t.Fatalf("Parse: %v", err)
	}
}

// Validation reports the whole list, because a person fixing a file would
// rather see it once than one save at a time.
func TestValidateReportsEveryProblemAtOnce(t *testing.T) {
	raw := strings.NewReplacer(
		"impact: critical", "impact: extreme",
		"default_lane: guarded", "default_lane: paranoid",
		"weights: [50, 100]", "weights: [50, 75]",
	).Replace(golden)

	err := parseErr(t, raw)
	for _, code := range []string{"invalid_impact", "undeclared_lane", "lane_does_not_finish"} {
		assertCode(t, err, code)
	}
}

func TestLaneForFallsBackToTheDefaultLane(t *testing.T) {
	cfg, err := config.Parse([]byte(golden))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	name, lane, err := cfg.Policy.LaneFor(config.RiskLow)
	if err != nil || name != "fast" || len(lane.Weights) != 2 {
		t.Fatalf("LaneFor(low) = %q, %v, %v", name, lane, err)
	}

	// No assessment at all is an expected case, and it picks the cautious lane
	// rather than failing or widening.
	name, lane, err = cfg.Policy.LaneFor("")
	if err != nil || name != "guarded" {
		t.Fatalf("LaneFor(\"\") = %q, %v, %v", name, lane, err)
	}
	if got := lane.Gates(); got != 3 {
		t.Errorf("guarded gates = %d, want 3 (four weights, three stops)", got)
	}
}

func TestDefaultPolicyValidates(t *testing.T) {
	cfg := config.Config{
		Application:  config.Application{Name: "payments-api", Repository: "acme/payments-api"},
		Artifact:     config.Artifact{Container: "payments-api", Image: "ghcr.io/acme/payments-api"},
		Environments: []config.Environment{{Name: "production", Impact: config.ImpactCritical, Kubernetes: config.Kubernetes{Context: "c", Namespace: "n", Rollout: "r"}}},
		Policy:       config.DefaultPolicy(),
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the compiled defaults do not validate: %v", err)
	}
}

func TestLoadOnAMissingFileSaysRegisterAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "apps", "payments-api", "safelane.yml")
	_, err := config.Load(path)
	assertCode(t, err, "unregistered_application")
	assertRemedy(t, err, "Register this application again")
}

// parseErr asserts that a document is refused, and returns the refusal.
func parseErr(t *testing.T, raw string) error {
	t.Helper()
	cfg, err := config.Parse([]byte(raw))
	if err == nil {
		t.Fatalf("Parse accepted the document: %+v", cfg)
	}
	return err
}

func assertCode(t *testing.T, err error, code string) {
	t.Helper()
	for _, e := range release.Flatten(err) {
		if e.Code == code {
			return
		}
	}
	t.Errorf("want a rejection with code %q, got %v", code, err)
}

// assertRemedy checks the one sentence a person is actually told to act on.
// It is a separate field from the message, and Error() does not print it.
func assertRemedy(t *testing.T, err error, want string) {
	t.Helper()
	for _, e := range release.Flatten(err) {
		if strings.Contains(e.Remedy, want) {
			return
		}
	}
	t.Errorf("want a remedy containing %q, got %v", want, err)
}

func assertMentions(t *testing.T, err error, substring string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), substring) {
		t.Errorf("want the reason to mention %q, got %v", substring, err)
	}
}

func quote(value string) string { return `"` + value + `"` }
