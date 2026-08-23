package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AndrewMaged814/safelane/internal/config"
	"github.com/AndrewMaged814/safelane/internal/discovery"
)

// fakeCluster is a kubectl that answers from a map, keyed by the exact
// argument line.
type fakeCluster map[string]string

func (f fakeCluster) run(_ context.Context, args []string) ([]byte, error) {
	line := strings.Join(args, " ")
	if out, ok := f[line]; ok {
		return []byte(out), nil
	}
	return nil, fmt.Errorf("Error from server (NotFound): %s", line)
}

// paymentsCluster is the cluster Appendix A1's example describes, so the
// golden can be compared literally rather than through placeholders.
func paymentsCluster() fakeCluster {
	rollout := `{
      "metadata": {"name": "payments-api", "namespace": "payments"},
      "spec": {
        "strategy": {"canary": {
          "stableService": "payments-api-stable",
          "canaryService": "payments-api-canary",
          "analysis": {"templates": [{"templateName": "success-rate"}]}
        }},
        "template": {"spec": {"containers": [
          {"name": "payments-api", "image": "ghcr.io/acme/payments-api@sha256:1111111111111111111111111111111111111111111111111111111111111111"}
        ]}}
      }
    }`
	template := `{
      "metadata": {"name": "success-rate", "namespace": "payments"},
      "spec": {"metrics": [{"name": "success-rate", "provider": {"prometheus": {"address": "http://prometheus:9090", "query": "up"}}}]}
    }`
	return fakeCluster{
		"config current-context":                                    "safelane-caller-payments-api",
		"get rollouts.argoproj.io -n payments -o json":              `{"items":[` + rollout + `]}`,
		"get rollouts.argoproj.io payments-api -n payments -o json": rollout,
		"get service payments-api-stable -n payments -o json":       `{"metadata":{"name":"payments-api-stable"}}`,
		"get service payments-api-canary -n payments -o json":       `{"metadata":{"name":"payments-api-canary"}}`,
		"get analysistemplate success-rate -o json -n payments":     template,
	}
}

func paymentsService(cluster fakeCluster) discovery.Service {
	return discovery.Service{
		Run:    cluster.run,
		Origin: func(string) (string, error) { return "acme/payments-api", nil },
	}
}

func writeSelection(t *testing.T, svc discovery.Service, mutate func(*discovery.Selection)) string {
	t.Helper()
	target, err := svc.Inspect(context.Background(), ".", "payments", "payments-api")
	if err != nil {
		t.Fatal(err)
	}
	selection := discovery.Selection{
		Application: "payments-api",
		Environment: "production",
		Impact:      config.ImpactCritical,
		Context:     target.Context,
		Namespace:   "payments",
		Rollout:     "payments-api",
		Container:   "payments-api",
		Fingerprint: target.Fingerprint,
	}
	if mutate != nil {
		mutate(&selection)
	}
	raw, err := json.Marshal(selection)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "selection.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The registration summary is Appendix A1, copied out of the plan. A mismatch
// is a defect in the code, never a reason to edit the golden.
func TestRegisterPrintsA1(t *testing.T) {
	svc := paymentsService(paymentsCluster())
	var stdout, stderr bytes.Buffer

	code := Register(context.Background(), RegisterOptions{
		Root:          ".",
		Home:          t.TempDir(),
		SelectionPath: writeSelection(t, svc, nil),
		ForceJSON:     false,
		Service:       svc,
	}, &terminal{&stdout}, &stderr)

	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	assertGolden(t, "registration-a1.txt", stdout.String())
}

// Rule 1: no flag on either side. A pipe gets JSON; a terminal gets text.
func TestRegisterIsJSONWhenPipedAndTextAtATerminal(t *testing.T) {
	svc := paymentsService(paymentsCluster())
	selection := writeSelection(t, svc, nil)

	var piped, stderr bytes.Buffer
	if code := Register(context.Background(), RegisterOptions{
		Root: ".", Home: t.TempDir(), SelectionPath: selection, Service: svc,
	}, &piped, &stderr); code != ExitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	var result map[string]any
	if err := json.Unmarshal(piped.Bytes(), &result); err != nil {
		t.Fatalf("piped output is not JSON: %v\n%s", err, piped.String())
	}
	if result["application"] != "payments-api" || result["environment"] != "production" {
		t.Errorf("result = %v", result)
	}
	// The machine form carries the file, so an agent shows the same preview a
	// person saw without reading the disk.
	if file, _ := result["file"].(string); !strings.Contains(file, "application:\n  name: payments-api") {
		t.Errorf("file = %q", file)
	}

	var text bytes.Buffer
	if code := Register(context.Background(), RegisterOptions{
		Root: ".", Home: t.TempDir(), SelectionPath: selection, Service: svc,
	}, &terminal{&text}, &stderr); code != ExitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !strings.HasPrefix(text.String(), "Registered payments-api in production.") {
		t.Errorf("terminal output = %q", text.String())
	}
}

// --json is the one way to get machine output at a terminal.
func TestForcedJSONAtATerminal(t *testing.T) {
	svc := paymentsService(paymentsCluster())
	var stdout, stderr bytes.Buffer
	if code := Register(context.Background(), RegisterOptions{
		Root: ".", Home: t.TempDir(), SelectionPath: writeSelection(t, svc, nil),
		ForceJSON: true, Service: svc,
	}, &terminal{&stdout}, &stderr); code != ExitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !strings.HasPrefix(strings.TrimSpace(stdout.String()), "{") {
		t.Errorf("--json did not force machine output: %q", stdout.String())
	}
}

func TestRegisterReadsTheSelectionFromStdin(t *testing.T) {
	svc := paymentsService(paymentsCluster())
	raw, err := os.ReadFile(writeSelection(t, svc, nil))
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := Register(context.Background(), RegisterOptions{
		Root: ".", Home: t.TempDir(), SelectionPath: "-", Service: svc,
		Stdin: bytes.NewReader(raw),
	}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
}

// A selection carrying a field discovery never asked about is refused, not
// quietly ignored.
func TestRegisterRefusesAnUnknownSelectionField(t *testing.T) {
	svc := paymentsService(paymentsCluster())
	var stdout, stderr bytes.Buffer
	code := Register(context.Background(), RegisterOptions{
		Root: ".", Home: t.TempDir(), SelectionPath: "-", Service: svc,
		Stdin: strings.NewReader(`{"application":"payments-api","lane":"fast"}`),
	}, &stdout, &stderr)
	if code == ExitOK {
		t.Fatal("register accepted a field it never asked for")
	}
	if !strings.Contains(stderr.String(), "lane") {
		t.Errorf("the refusal should name the field: %s", stderr.String())
	}
}

// Rule 3: the Application is inferred, and naming it is required only when the
// inference is not unique.
func TestApplicationIsInferredFromTheGitOrigin(t *testing.T) {
	home := t.TempDir()
	writeRegisteredApp(t, home, "payments-api", "acme/payments-api")

	got, err := ResolveApplication(home, "acme/payments-api", "")
	if err != nil {
		t.Fatalf("ResolveApplication: %v", err)
	}
	if got != "payments-api" {
		t.Errorf("application = %q", got)
	}
}

func TestNamingTheApplicationIsRequiredOnlyWhenAmbiguous(t *testing.T) {
	home := t.TempDir()
	writeRegisteredApp(t, home, "payments-api", "acme/payments-api")
	writeRegisteredApp(t, home, "payments-worker", "acme/payments-api")

	_, err := ResolveApplication(home, "acme/payments-api", "")
	if err == nil {
		t.Fatal("two matches resolved to one application")
	}
	if !strings.Contains(err.Error(), "payments-api") || !strings.Contains(err.Error(), "payments-worker") {
		t.Errorf("the refusal should name both: %v", err)
	}

	got, err := ResolveApplication(home, "acme/payments-api", "payments-worker")
	if err != nil || got != "payments-worker" {
		t.Errorf("--app did not disambiguate: %q %v", got, err)
	}
}

func writeRegisteredApp(t *testing.T, home, app, repository string) {
	t.Helper()
	file := config.Render(config.Discovered{
		Application: config.Application{Name: app, Repository: repository},
		Artifact:    config.Artifact{Container: app, Image: "ghcr.io/" + repository},
		Environment: config.Environment{
			Name:       "production",
			Impact:     config.ImpactCritical,
			Kubernetes: config.Kubernetes{Context: "c", Namespace: "n", Rollout: app},
		},
	}, config.DefaultPolicy())
	if _, err := config.Write(config.ForApp(home, app).File, file); err != nil {
		t.Fatal(err)
	}
}

// terminal is a buffer that claims to be a tty, so the readable branch of rule
// 1 can be exercised - and golden-tested - without a real one.
type terminal struct{ *bytes.Buffer }

func (terminal) IsTerminal() bool { return true }
