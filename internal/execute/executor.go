package execute

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/AndrewMaged814/safelane/internal/release"
)

// Runner runs one kubectl invocation and returns its stdout. It is the
// single seam every call in this package goes through -- privileged and
// unprivileged alike -- so the whole executor is testable against canned
// output with no cluster (Appendix D).
type Runner func(ctx context.Context, args []string, stdin []byte) ([]byte, error)

// realRun is the production [Runner]: it shells out to the kubectl binary
// on PATH.
func realRun(ctx context.Context, args []string, stdin []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return stdout.Bytes(), fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return stdout.Bytes(), err
	}
	return stdout.Bytes(), nil
}

// Config names the target Rollout and, when setup has configured
// separate identities (Appendix C5, tasks 14/20), the privileged
// controller credential.
//
// ControllerKubeconfig and ControllerContext are both optional. Unset,
// every privileged call runs against kubectl's ambient context, the same
// as an unprivileged one -- this package threads the flags when they are
// configured and works without them when they are not, rather than
// blocking on the two identities existing.
type Config struct {
	Namespace            string
	Rollout              string
	ControllerKubeconfig string
	ControllerContext    string
}

// Executor drives Argo Rollouts through kubectl.
type Executor struct {
	Config
	// Run is the injected command seam. Production wiring uses realRun;
	// tests substitute a fake that returns canned output.
	Run Runner
	// Now and Sleep drive WaitForGate's polling loop. Both default to the
	// real clock; tests override them so a timeout or a multi-poll
	// sequence advances instantly instead of waiting on the wall clock.
	Now   func() time.Time
	Sleep func(time.Duration)
	// PollInterval is how often WaitForGate polls kubectl. 2s by default,
	// per Appendix C5.
	PollInterval time.Duration
}

// New builds an Executor wired to the real kubectl binary and the real
// clock.
func New(cfg Config) *Executor {
	return &Executor{
		Config:       cfg,
		Run:          realRun,
		Now:          time.Now,
		Sleep:        time.Sleep,
		PollInterval: 2 * time.Second,
	}
}

func (e *Executor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Executor) sleep(d time.Duration) {
	if e.Sleep != nil {
		e.Sleep(d)
		return
	}
	time.Sleep(d)
}

func (e *Executor) pollInterval() time.Duration {
	if e.PollInterval > 0 {
		return e.PollInterval
	}
	return 2 * time.Second
}

// privilegedFlags is the `--kubeconfig <cc> --context <ctx>` pair Appendix
// C5 requires on every privileged call, added only for the fields that are
// actually configured.
func (e *Executor) privilegedFlags() []string {
	var out []string
	if e.ControllerKubeconfig != "" {
		out = append(out, "--kubeconfig", e.ControllerKubeconfig)
	}
	if e.ControllerContext != "" {
		out = append(out, "--context", e.ControllerContext)
	}
	return out
}

// run executes one kubectl invocation through the injected Runner and
// classifies a failure into a human-readable, Appendix C4 reason rather
// than letting a raw *exec.Error or a stack trace reach the caller.
func (e *Executor) run(ctx context.Context, verb string, args []string, stdin []byte) ([]byte, error) {
	out, err := e.Run(ctx, args, stdin)
	if err != nil {
		return nil, classifyRunError(verb, err)
	}
	return out, nil
}

// classifyRunError turns a Runner failure into an actionable *release.Error.
// A missing kubectl binary is a distinct, non-retryable defect (install
// it); anything else observed while trying to reach the cluster is
// reported as unreachable, which Appendix C4 marks retryable.
func classifyRunError(verb string, err error) error {
	if errors.Is(err, exec.ErrNotFound) {
		return release.Invalid("kubectl_missing", "",
			"kubectl was not found on PATH",
			"Install kubectl (and the argo rollouts plugin, for promote/abort/pause) and retry.")
	}
	return release.Invalid("cluster_unreachable", "",
		fmt.Sprintf("%s failed: %v", verb, err),
		"Check that the cluster is reachable and the kubeconfig/context are correct, then retry.")
}

// ApplyPatch sends the Release Patch to the API server.
//
// `--type=json` and nothing else: the patch is the two replacements and the
// two tests that guard them, and the API server rejects the whole document if
// either test fails. SafeLane never sends a whole object, so there is no path
// by which a field it did not name could be overwritten by a stale copy of the
// Rollout.
//
// This replaces the old bundle apply, which sent every rendered resource and
// therefore made every field in them SafeLane's opinion.
func (e *Executor) ApplyPatch(ctx context.Context, patch []byte) error {
	args := append([]string{"patch", "rollout", e.Rollout, "-n", e.Namespace,
		"--type=json", "-p", string(patch)}, e.privilegedFlags()...)
	_, err := e.run(ctx, "kubectl patch rollout", args, nil)
	return err
}

// AnnotateRelease binds the controller-owned Rollout to exactly one SafeLane
// attempt, so a Rollout somebody else's release already owns can be recognised
// as one rather than quietly shared.
func (e *Executor) AnnotateRelease(ctx context.Context, id string) error {
	args := append([]string{"annotate", "rollout", e.Rollout, "-n", e.Namespace,
		"safelane.dev/release-id=" + id, "--overwrite"}, e.privilegedFlags()...)
	_, err := e.run(ctx, "kubectl annotate rollout", args, nil)
	return err
}

// Retry restarts an explicitly aborted same-artifact Rollout without reapplying it.
func (e *Executor) Retry(ctx context.Context) error {
	args := append([]string{"argo", "rollouts", "retry", "rollout", e.Rollout, "-n", e.Namespace}, e.privilegedFlags()...)
	_, err := e.run(ctx, "kubectl argo rollouts retry", args, nil)
	return err
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// Promote runs `kubectl argo rollouts promote`, advancing the Rollout past
// its current pause (Appendix C5). It is a privileged call: it carries the
// controller identity, the same as Apply.
//
// It never generates `--full` -- that flag skips every remaining step and
// jumps straight to 100%, which would silently defeat every lane. Nothing
// in this function ever appends it, and executor_test.go asserts that no
// call this package makes ever does.
func (e *Executor) Promote(ctx context.Context) error {
	args := append([]string{"argo", "rollouts", "promote", e.Rollout, "-n", e.Namespace}, e.privilegedFlags()...)
	_, err := e.run(ctx, "kubectl argo rollouts promote", args, nil)
	return err
}

// Pause runs `kubectl argo rollouts pause` (Appendix C5), holding the
// Rollout exactly where it is. Like Promote, it is a privileged call and
// never generates `--full` -- there is nothing for that flag to mean here,
// but it goes through the same run helper as every other call regardless.
func (e *Executor) Pause(ctx context.Context) error {
	args := append([]string{"argo", "rollouts", "pause", e.Rollout, "-n", e.Namespace}, e.privilegedFlags()...)
	_, err := e.run(ctx, "kubectl argo rollouts pause", args, nil)
	return err
}

// Abort runs `kubectl argo rollouts abort` (Appendix C5): the caller's own
// abort, distinct from Argo Rollouts deciding to abort on its own after a
// failed analysis (which SafeLane only ever observes, never issues). It
// restores stable traffic; SafeLane's own job is recording who asked and
// why, not the traffic shift itself.
func (e *Executor) Abort(ctx context.Context) error {
	args := append([]string{"argo", "rollouts", "abort", e.Rollout, "-n", e.Namespace}, e.privilegedFlags()...)
	_, err := e.run(ctx, "kubectl argo rollouts abort", args, nil)
	return err
}

// lastField returns the last whitespace-separated token of a line, which
// is where every kubectl apply outcome word ("unchanged", "created",
// "configured") lands regardless of the resource-type prefix before it.
func lastField(line string) string {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}
