package execute_test

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/AndrewMaged814/safelane/internal/execute"
	"github.com/AndrewMaged814/safelane/internal/release"
)

// fakeRunner is Appendix D's "fake cmdFactory returning canned Argo JSON":
// every call this package makes goes through it, so none of these tests
// touch a real cluster. Responses are consumed in call order; calls are
// recorded so a test can assert on the exact argument list.
type fakeRunner struct {
	calls     [][]string
	stdins    [][]byte
	responses [][]byte
	errs      []error
	i         int
}

func (f *fakeRunner) run(ctx context.Context, args []string, stdin []byte) ([]byte, error) {
	f.calls = append(f.calls, append([]string{}, args...))
	f.stdins = append(f.stdins, stdin)
	if f.i >= len(f.responses) {
		return nil, errors.New("fakeRunner: no more canned responses")
	}
	out, err := f.responses[f.i], f.errs[f.i]
	f.i++
	return out, err
}

func (f *fakeRunner) enqueue(out string, err error) {
	f.responses = append(f.responses, []byte(out))
	f.errs = append(f.errs, err)
}

// testPatch is the shape releasepatch produces: two tests and two replaces.
var testPatch = []byte(`[` +
	`{"op":"test","path":"/metadata/resourceVersion","value":"84213"},` +
	`{"op":"test","path":"/spec/template/spec/containers/0/image","value":"ghcr.io/o/i@sha256:old"},` +
	`{"op":"replace","path":"/spec/template/spec/containers/0/image","value":"ghcr.io/o/i@sha256:new"},` +
	`{"op":"replace","path":"/spec/strategy/canary/steps","value":[{"setWeight":50},{"pause":{}}]}` +
	`]`)

func newTestExecutor(fr *fakeRunner) *execute.Executor {
	ex := execute.New(execute.Config{Namespace: "safelane-demo-api", Rollout: "safelane-demo-api"})
	ex.Run = fr.run
	ex.Sleep = func(time.Duration) {} // instant: WaitForGate tests drive time via Now
	return ex
}

func TestReleaseAnnotationAndArgoRetryUseNarrowExactCommands(t *testing.T) {
	fr := &fakeRunner{}
	fr.enqueue("annotated\n", nil)
	fr.enqueue("retried\n", nil)
	ex := newTestExecutor(fr)
	id := "rel_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	if err := ex.AnnotateRelease(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if err := ex.Retry(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantAnnotation := "annotate rollout safelane-demo-api -n safelane-demo-api safelane.dev/release-id=" + string(id) + " --overwrite"
	if got := strings.Join(fr.calls[0], " "); got != wantAnnotation {
		t.Fatalf("annotation command = %q", got)
	}
	if got := strings.Join(fr.calls[1], " "); got != "argo rollouts retry rollout safelane-demo-api -n safelane-demo-api" {
		t.Fatalf("retry command = %q", got)
	}
}

func TestPrivilegedFlags_ThreadedOnlyWhenConfigured(t *testing.T) {
	fr := &fakeRunner{}
	fr.enqueue("service/safelane-demo-api-stable unchanged\nrollout.argoproj.io/safelane-demo-api unchanged\n", nil)
	ex := execute.New(execute.Config{
		Namespace: "safelane-demo-api", Rollout: "safelane-demo-api",
		ControllerKubeconfig: "controller.kubeconfig", ControllerContext: "safelane-controller",
	})
	ex.Run = fr.run

	if err := ex.ApplyPatch(context.Background(), testPatch); err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	args := strings.Join(fr.calls[0], " ")
	if !strings.Contains(args, "--kubeconfig controller.kubeconfig") || !strings.Contains(args, "--context safelane-controller") {
		t.Errorf("privileged patch args = %v, want the controller kubeconfig/context threaded through", fr.calls[0])
	}
}

func TestPrivilegedFlags_AbsentWhenNotConfigured(t *testing.T) {
	fr := &fakeRunner{}
	fr.enqueue("service/safelane-demo-api-stable unchanged\nrollout.argoproj.io/safelane-demo-api unchanged\n", nil)
	ex := newTestExecutor(fr)

	if err := ex.ApplyPatch(context.Background(), testPatch); err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	for _, a := range fr.calls[0] {
		if a == "--kubeconfig" || a == "--context" {
			t.Errorf("patch args = %v, want no controller flags when none are configured", fr.calls[0])
		}
	}
}

func TestGetStatus_NeverCarriesTheControllerFlags(t *testing.T) {
	fr := &fakeRunner{}
	fr.enqueue(`{"status":{"phase":"Progressing"}}`, nil)
	ex := execute.New(execute.Config{
		Namespace: "safelane-demo-api", Rollout: "safelane-demo-api",
		ControllerKubeconfig: "controller.kubeconfig", ControllerContext: "safelane-controller",
	})
	ex.Run = fr.run

	if _, err := ex.GetStatus(context.Background()); err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	for _, a := range fr.calls[0] {
		if a == "--kubeconfig" || a == "--context" {
			t.Errorf("get rollout args = %v, want no controller flags -- caller identity is enough (Appendix C5)", fr.calls[0])
		}
	}
}

func TestArguments_NeverContainFull(t *testing.T) {
	// The one flag this package must never generate: `--full` jumps
	// straight to 100% and would silently defeat every lane.
	fr := &fakeRunner{}
	fr.enqueue("service/safelane-demo-api-stable unchanged\nrollout.argoproj.io/safelane-demo-api unchanged\n", nil)
	fr.enqueue(`{"status":{"phase":"Progressing"}}`, nil)
	fr.enqueue("rollout.argoproj.io/safelane-demo-api promoted\n", nil)
	ex := newTestExecutor(fr)

	if err := ex.ApplyPatch(context.Background(), testPatch); err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	if _, err := ex.GetStatus(context.Background()); err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if err := ex.Promote(context.Background()); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	for _, call := range fr.calls {
		for _, a := range call {
			if a == "--full" {
				t.Fatalf("generated argument list %v contains --full", call)
			}
		}
	}
}

func TestPromote_IsArgoRolloutsPromoteWithTheControllerFlags(t *testing.T) {
	fr := &fakeRunner{}
	fr.enqueue("rollout.argoproj.io/safelane-demo-api promoted\n", nil)
	ex := execute.New(execute.Config{
		Namespace: "safelane-demo-api", Rollout: "safelane-demo-api",
		ControllerKubeconfig: "controller.kubeconfig", ControllerContext: "safelane-controller",
	})
	ex.Run = fr.run

	if err := ex.Promote(context.Background()); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	got := strings.Join(fr.calls[0], " ")
	want := "argo rollouts promote safelane-demo-api -n safelane-demo-api --kubeconfig controller.kubeconfig --context safelane-controller"
	if got != want {
		t.Errorf("promote args = %q, want %q", got, want)
	}
}

func TestPromote_ClassifiesAFailureLikeEveryOtherCall(t *testing.T) {
	fr := &fakeRunner{}
	fr.enqueue("", &exec.Error{Name: "kubectl", Err: exec.ErrNotFound})
	ex := newTestExecutor(fr)

	err := ex.Promote(context.Background())
	var rerr *release.Error
	if !errors.As(err, &rerr) || rerr.Code != "kubectl_missing" {
		t.Fatalf("err = %v, want a kubectl_missing *release.Error", err)
	}
}

func TestClassifyRunError_MissingBinaryIsHumanReadable(t *testing.T) {
	fr := &fakeRunner{}
	fr.enqueue("", &exec.Error{Name: "kubectl", Err: exec.ErrNotFound})
	ex := newTestExecutor(fr)

	err := ex.ApplyPatch(context.Background(), testPatch)
	if err == nil {
		t.Fatal("want an error when kubectl is missing")
	}
	var rerr *release.Error
	if !errors.As(err, &rerr) {
		t.Fatalf("error = %v (%T), want a *release.Error, not a raw stack trace", err, err)
	}
	if rerr.Code != "kubectl_missing" {
		t.Errorf("code = %q, want kubectl_missing", rerr.Code)
	}
}

func TestClassifyRunError_OtherFailureIsClusterUnreachable(t *testing.T) {
	fr := &fakeRunner{}
	fr.enqueue("", errors.New("dial tcp: connection refused"))
	ex := newTestExecutor(fr)

	err := ex.ApplyPatch(context.Background(), testPatch)
	var rerr *release.Error
	if !errors.As(err, &rerr) {
		t.Fatalf("error = %v (%T), want a *release.Error", err, err)
	}
	if rerr.Code != "cluster_unreachable" {
		t.Errorf("code = %q, want cluster_unreachable", rerr.Code)
	}
}

func TestPause_IsArgoRolloutsPauseWithTheControllerFlags(t *testing.T) {
	fr := &fakeRunner{}
	fr.enqueue("rollout.argoproj.io/safelane-demo-api paused\n", nil)
	ex := execute.New(execute.Config{
		Namespace: "safelane-demo-api", Rollout: "safelane-demo-api",
		ControllerKubeconfig: "controller.kubeconfig", ControllerContext: "safelane-controller",
	})
	ex.Run = fr.run

	if err := ex.Pause(context.Background()); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	got := strings.Join(fr.calls[0], " ")
	want := "argo rollouts pause safelane-demo-api -n safelane-demo-api --kubeconfig controller.kubeconfig --context safelane-controller"
	if got != want {
		t.Errorf("pause args = %q, want %q", got, want)
	}
}

func TestPause_ClassifiesAFailureLikeEveryOtherCall(t *testing.T) {
	fr := &fakeRunner{}
	fr.enqueue("", &exec.Error{Name: "kubectl", Err: exec.ErrNotFound})
	ex := newTestExecutor(fr)

	err := ex.Pause(context.Background())
	var rerr *release.Error
	if !errors.As(err, &rerr) || rerr.Code != "kubectl_missing" {
		t.Fatalf("err = %v, want a kubectl_missing *release.Error", err)
	}
}

func TestAbort_IsArgoRolloutsAbortWithTheControllerFlags(t *testing.T) {
	fr := &fakeRunner{}
	fr.enqueue("rollout.argoproj.io/safelane-demo-api aborted\n", nil)
	ex := execute.New(execute.Config{
		Namespace: "safelane-demo-api", Rollout: "safelane-demo-api",
		ControllerKubeconfig: "controller.kubeconfig", ControllerContext: "safelane-controller",
	})
	ex.Run = fr.run

	if err := ex.Abort(context.Background()); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	got := strings.Join(fr.calls[0], " ")
	want := "argo rollouts abort safelane-demo-api -n safelane-demo-api --kubeconfig controller.kubeconfig --context safelane-controller"
	if got != want {
		t.Errorf("abort args = %q, want %q", got, want)
	}
}

func TestAbort_ClassifiesAFailureLikeEveryOtherCall(t *testing.T) {
	fr := &fakeRunner{}
	fr.enqueue("", &exec.Error{Name: "kubectl", Err: exec.ErrNotFound})
	ex := newTestExecutor(fr)

	err := ex.Abort(context.Background())
	var rerr *release.Error
	if !errors.As(err, &rerr) || rerr.Code != "kubectl_missing" {
		t.Fatalf("err = %v, want a kubectl_missing *release.Error", err)
	}
}

func TestPauseAndAbort_NeverGenerateFull(t *testing.T) {
	fr := &fakeRunner{}
	fr.enqueue("rollout.argoproj.io/safelane-demo-api paused\n", nil)
	fr.enqueue("rollout.argoproj.io/safelane-demo-api aborted\n", nil)
	ex := newTestExecutor(fr)

	if err := ex.Pause(context.Background()); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if err := ex.Abort(context.Background()); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	for _, call := range fr.calls {
		for _, a := range call {
			if a == "--full" {
				t.Fatalf("generated argument list %v contains --full", call)
			}
		}
	}
}
