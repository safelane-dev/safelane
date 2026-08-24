package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/AndrewMaged814/safelane/internal/config"
	"github.com/AndrewMaged814/safelane/internal/releasepatch"
)

// recommended runs a proceeding recommendation so there is something awaiting
// approval, and returns the options that address it.
func recommended(t *testing.T) InspectOptions {
	t.Helper()
	opts := inspectOptions(t)
	raw := a2Assessment(t, snapshotFor(t, opts))

	var stdout, stderr bytes.Buffer
	if code := Recommend(context.Background(), RecommendOptions{
		Inspect: opts, AssessmentPath: "-", Stdin: bytes.NewReader(raw),
	}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("recommend: %s", stderr.String())
	}
	return opts
}

func environmentDir(t *testing.T, opts InspectOptions) string {
	t.Helper()
	return config.ForApp(opts.Home, "payments-api").ForEnvironment("production").Dir
}

func runOptions(opts InspectOptions) RunOptions {
	return RunOptions{
		Inspect: opts,
		Now:     func() time.Time { return time.Date(2026, 8, 21, 12, 30, 0, 0, time.UTC) },
	}
}

// `run` never assesses. Assessing and approving are separate acts, and a run
// that quietly assessed first would collapse them.
func TestRunRefusesWhenNothingIsAwaitingApproval(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), runOptions(inspectOptions(t)), &stdout, &stderr)
	if code == ExitOK {
		t.Fatal("run released with no recommendation awaiting approval")
	}
	if !strings.Contains(stderr.String(), "no recommendation waiting for approval") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunRefusesAWaitingRecommendation(t *testing.T) {
	opts := inspectOptions(t)
	raw := a3Assessment(t, snapshotFor(t, opts))
	var out, errs bytes.Buffer
	if code := Recommend(context.Background(), RecommendOptions{
		Inspect: opts, AssessmentPath: "-", Stdin: bytes.NewReader(raw),
	}, &out, &errs); code != ExitOK {
		t.Fatalf("recommend: %s", errs.String())
	}

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), runOptions(opts), &stdout, &stderr); code == ExitOK {
		t.Fatal("run released a waiting recommendation")
	}
	if !strings.Contains(stderr.String(), "recommendation for payments-api in production is to wait") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

// Piped, the frozen recommendation is the authorization and nothing is asked.
// A flag the caller passes on every invocation is not a safety mechanism.
func TestRunDoesNotAskWhenPiped(t *testing.T) {
	opts := recommended(t)
	applied := 0

	var stdout, stderr bytes.Buffer
	run := runOptions(opts)
	run.Apply = func(context.Context, releasepatch.Patch) error { applied++; return nil }

	if code := Run(context.Background(), run, &stdout, &stderr); code != ExitOK {
		t.Fatalf("exit: %s", stderr.String())
	}
	if applied != 1 {
		t.Errorf("applied %d times", applied)
	}
	if strings.Contains(stdout.String(), "Proceed with this rollout") {
		t.Errorf("a piped run asked a question:\n%s", stdout.String())
	}
}

// At a terminal, `run` asks before it mutates, using A5.
func TestRunAsksAtATerminal(t *testing.T) {
	opts := recommended(t)
	applied := 0

	var stdout, stderr bytes.Buffer
	run := runOptions(opts)
	run.Confirm = strings.NewReader("yes\n")
	run.Apply = func(context.Context, releasepatch.Patch) error { applied++; return nil }

	if code := Run(context.Background(), run, &terminal{&stdout}, &stderr); code != ExitOK {
		t.Fatalf("exit: %s", stderr.String())
	}
	if !strings.Contains(stdout.String(), "Proceed with this rollout to production?") {
		t.Errorf("run did not ask:\n%s", stdout.String())
	}
	if applied != 1 {
		t.Errorf("applied %d times", applied)
	}
}

func TestAnAnswerThatIsNotApprovalChangesNothing(t *testing.T) {
	opts := recommended(t)
	applied := 0

	var stdout, stderr bytes.Buffer
	run := runOptions(opts)
	run.Confirm = strings.NewReader("not yet\n")
	run.Apply = func(context.Context, releasepatch.Patch) error { applied++; return nil }

	if code := Run(context.Background(), run, &terminal{&stdout}, &stderr); code != ExitOK {
		t.Fatalf("exit: %s", stderr.String())
	}
	if applied != 0 {
		t.Error("the cluster was touched without approval")
	}
	if !strings.Contains(stdout.String(), "Nothing was changed.") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

// An approval that could be applied twice is not an approval of one release.
func TestAnApprovalCannotBeSpentTwice(t *testing.T) {
	opts := recommended(t)
	applied := 0
	run := runOptions(opts)
	run.Apply = func(context.Context, releasepatch.Patch) error { applied++; return nil }

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), run, &stdout, &stderr); code != ExitOK {
		t.Fatalf("first run: %s", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(context.Background(), run, &stdout, &stderr); code == ExitOK {
		t.Fatal("the approval was spent twice")
	}
	if !strings.Contains(stderr.String(), "already used") {
		t.Errorf("stderr = %q", stderr.String())
	}
	if applied != 1 {
		t.Errorf("applied %d times", applied)
	}
}

// The recheck runs immediately before the cluster is touched. The gap between
// approval and apply is exactly where a digest moves.
func TestAMaterialChangeCancelsTheApprovalAndChangesNothing(t *testing.T) {
	opts := recommended(t)

	// Somebody else deployed in the meantime.
	pending, found, err := releasepatch.LoadPending(environmentDir(t, opts))
	if err != nil || !found {
		t.Fatalf("pending = %v %v", found, err)
	}
	pending.Facts.RunningImage = "ghcr.io/acme/payments-api@sha256:" + strings.Repeat("e", 64)
	if err := releasepatch.SavePending(environmentDir(t, opts), pending); err != nil {
		t.Fatal(err)
	}

	applied := 0
	run := runOptions(opts)
	run.Apply = func(context.Context, releasepatch.Patch) error { applied++; return nil }

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), run, &stdout, &stderr); code == ExitOK {
		t.Fatal("run proceeded after a material change")
	}
	if applied != 0 {
		t.Error("the cluster was touched after the approval was cancelled")
	}
	if !strings.Contains(stderr.String(), "no longer applies") {
		t.Errorf("stderr = %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "something else deployed") {
		t.Errorf("the cancellation should say what moved: %q", stderr.String())
	}
}

// The pending recommendation carries the exact patch, so what is approved and
// what is applied are the same object.
func TestThePendingRecommendationCarriesTheExactPatch(t *testing.T) {
	opts := recommended(t)

	pending, found, err := releasepatch.LoadPending(environmentDir(t, opts))
	if err != nil || !found {
		t.Fatalf("pending = %v %v", found, err)
	}
	if pending.Action != "proceed" || pending.Lane != "fast" {
		t.Errorf("pending = %+v", pending)
	}
	if len(pending.Patch.Operations) != 4 {
		t.Fatalf("operations = %+v", pending.Patch.Operations)
	}
	if pending.Patch.Operations[3].Path != "/spec/strategy/canary/steps" {
		t.Errorf("last operation = %+v", pending.Patch.Operations[3])
	}
	if pending.Patch.CandidateImage != "ghcr.io/acme/payments-api@"+candidateDigest {
		t.Errorf("candidate image = %q", pending.Patch.CandidateImage)
	}
	if pending.Facts.PatchDigest != pending.Patch.Digest() {
		t.Error("the recorded facts do not describe the recorded patch")
	}
	if len(pending.Analysis) != 1 || pending.Analysis[0] != "success-rate" {
		t.Errorf("analysis = %v", pending.Analysis)
	}
}

// The correction counter lives with the pending recommendation, so a session
// cannot restart it by resubmitting.
func TestTheCorrectionCounterSurvivesResubmission(t *testing.T) {
	opts := inspectOptions(t)
	broken := func() []byte {
		return []byte(`{"snapshot":"sha256:wrong","observations":[],"risk":"low","action":"proceed","lane":"fast","rationale":"x"}`)
	}

	var stdout, stderr bytes.Buffer
	if code := Recommend(context.Background(), RecommendOptions{
		Inspect: opts, AssessmentPath: "-", Stdin: bytes.NewReader(broken()),
	}, &stdout, &stderr); code != ExitDecision {
		t.Fatalf("first submission: exit %d", code)
	}

	// The second submission is the correction, and after it SafeLane waits.
	stdout.Reset()
	stderr.Reset()
	if code := Recommend(context.Background(), RecommendOptions{
		Inspect: opts, AssessmentPath: "-", Stdin: bytes.NewReader(broken()),
	}, &terminal{&stdout}, &stderr); code != ExitOK {
		t.Fatalf("second submission: exit %d: %s", code, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "I recommend waiting on this release.") {
		t.Errorf("stdout:\n%s", stdout.String())
	}
}

func TestRunIsJSONWhenPiped(t *testing.T) {
	opts := recommended(t)
	run := runOptions(opts)
	run.Apply = func(context.Context, releasepatch.Patch) error { return nil }

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), run, &stdout, &stderr); code != ExitOK {
		t.Fatalf("exit: %s", stderr.String())
	}
	var result struct {
		Application string                `json:"application"`
		Lane        string                `json:"lane"`
		Approval    releasepatch.Approval `json:"approval"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("piped output is not JSON: %v\n%s", err, stdout.String())
	}
	if result.Application != "payments-api" || result.Lane != "fast" {
		t.Errorf("result = %+v", result)
	}
	if result.Approval.UsedAt.IsZero() {
		t.Error("the approval was not recorded as spent")
	}
}
