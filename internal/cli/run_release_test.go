package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/AndrewMaged814/safelane/internal/config"
	"github.com/AndrewMaged814/safelane/internal/journal"
	"github.com/AndrewMaged814/safelane/internal/releasepatch"
	"github.com/AndrewMaged814/safelane/internal/verify/oci"
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

func approvePending(t *testing.T, opts InspectOptions, answer string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := Approve(context.Background(), ApproveOptions{
		Root: ".", Home: opts.Home, Environment: "production",
		Origin: func(string) (string, error) { return "acme/payments-api", nil },
		Answer: answer,
		Now:    func() time.Time { return time.Date(2026, 8, 21, 12, 29, 0, 0, time.UTC) },
	}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("approve: %s", stderr.String())
	}
}

func completingCoordinator(applied *int) func(context.Context, journal.Record, releasepatch.Patch) (journal.Record, error) {
	return func(_ context.Context, record journal.Record, _ releasepatch.Patch) (journal.Record, error) {
		if applied != nil {
			*applied++
		}
		record.State = journal.StateCompleted
		record.Weight = 100
		record.Outcome = "released"
		return record, nil
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

// A recommendation is advice, not authorization. The agent path is piped, so
// it must record the user's explicit answer before `run` may touch the cluster.
func TestRunRefusesWhenPipedWithoutExplicitApproval(t *testing.T) {
	opts := recommended(t)
	applied := 0

	var stdout, stderr bytes.Buffer
	run := runOptions(opts)
	run.Coordinate = completingCoordinator(&applied)

	if code := Run(context.Background(), run, &stdout, &stderr); code == ExitOK {
		t.Fatal("run released without an explicit approval")
	}
	if applied != 0 {
		t.Errorf("applied %d times", applied)
	}
	if !strings.Contains(stderr.String(), "explicit approval") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestAgentApprovalBindsThePendingRecommendationBeforeRun(t *testing.T) {
	opts := recommended(t)
	applied := 0
	approvePending(t, opts, "approve this")

	run := runOptions(opts)
	run.Coordinate = completingCoordinator(&applied)
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), run, &stdout, &stderr); code != ExitOK {
		t.Fatalf("run: %s", stderr.String())
	}
	if applied != 1 {
		t.Errorf("applied %d times", applied)
	}
}

// Assessment already froze source, CI, and artifact evidence. Immediately
// before mutation, run rechecks only facts that can still move: the live
// Rollout, its analysis, the running image, and local release settings.
func TestApprovedRunDoesNotRepeatAssessmentEvidenceReads(t *testing.T) {
	opts := recommended(t)
	approvePending(t, opts, "approve this")
	opts.Source = nil
	opts.Registry = oci.Resolver{}

	applied := 0
	run := runOptions(opts)
	run.Coordinate = completingCoordinator(&applied)
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), run, &stdout, &stderr); code != ExitOK {
		t.Fatalf("run repeated assessment evidence reads: %s", stderr.String())
	}
	if applied != 1 {
		t.Fatalf("applied %d times", applied)
	}
}

func TestRunStartsOneDurableAttachedRelease(t *testing.T) {
	opts := recommended(t)
	approvePending(t, opts, "approve this")
	called := 0
	run := runOptions(opts)
	run.Coordinate = func(_ context.Context, record journal.Record, patch releasepatch.Patch) (journal.Record, error) {
		called++
		if record.Application != "payments-api" || record.Environment != "production" {
			t.Fatalf("record = %+v", record)
		}
		if len(record.Delta) == 0 || len(record.Recommendation) == 0 || len(record.Approval) == 0 {
			t.Fatalf("release proof inputs are incomplete: %+v", record)
		}
		if patch.CandidateImage == "" {
			t.Fatal("coordinator received an empty patch")
		}
		record.State = journal.StateCompleted
		record.Weight = 100
		record.Outcome = "released"
		return record, nil
	}

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), run, &stdout, &stderr); code != ExitOK {
		t.Fatalf("run: %s", stderr.String())
	}
	if called != 1 {
		t.Fatalf("coordinator called %d times", called)
	}
}

func TestRunReconnectsAnActiveReleaseWithoutAnotherApproval(t *testing.T) {
	opts := recommended(t)
	approvePending(t, opts, "approve this")
	store := journal.Store{Dir: environmentDir(t, opts)}
	calls := 0
	run := runOptions(opts)
	run.Coordinate = func(_ context.Context, record journal.Record, _ releasepatch.Patch) (journal.Record, error) {
		calls++
		if calls == 1 {
			record.State = journal.StateMonitoring
			if _, err := store.Start(record); err != nil {
				t.Fatal(err)
			}
			return record, context.Canceled
		}
		record.State = journal.StateCompleted
		record.Weight = 100
		record.Outcome = "released"
		return record, nil
	}

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), run, &stdout, &stderr); code == ExitOK {
		t.Fatal("interrupted attached run reported completion")
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(context.Background(), run, &stdout, &stderr); code != ExitOK {
		t.Fatalf("reconnect: %s", stderr.String())
	}
	if calls != 2 {
		t.Fatalf("coordinator calls = %d, want 2", calls)
	}
}

// At a terminal, `run` asks before it mutates, using A5.
func TestRunAsksAtATerminal(t *testing.T) {
	opts := recommended(t)
	applied := 0

	var stdout, stderr bytes.Buffer
	run := runOptions(opts)
	run.Confirm = strings.NewReader("yes\n")
	run.Coordinate = completingCoordinator(&applied)

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
	run.Coordinate = completingCoordinator(&applied)

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
	approvePending(t, opts, "release it")
	run := runOptions(opts)
	run.Coordinate = completingCoordinator(&applied)

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), run, &stdout, &stderr); code != ExitOK {
		t.Fatalf("first run: %s", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(context.Background(), run, &stdout, &stderr); code == ExitOK {
		t.Fatal("the approval was spent twice")
	}
	if !strings.Contains(stderr.String(), "no recommendation waiting for approval") {
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
	approvePending(t, opts, "go ahead")

	applied := 0
	run := runOptions(opts)
	run.Coordinate = completingCoordinator(&applied)

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

func TestRunRechecksCandidateDigestAgainstTheExactPatch(t *testing.T) {
	opts := recommended(t)
	pending, found, err := releasepatch.LoadPending(environmentDir(t, opts))
	if err != nil || !found {
		t.Fatalf("pending = %v %v", found, err)
	}
	pending.Facts.Digest = "sha256:" + strings.Repeat("e", 64)
	if err := releasepatch.SavePending(environmentDir(t, opts), pending); err != nil {
		t.Fatal(err)
	}
	approvePending(t, opts, "go ahead")

	applied := 0
	run := runOptions(opts)
	run.Coordinate = completingCoordinator(&applied)
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), run, &stdout, &stderr); code == ExitOK {
		t.Fatal("run proceeded when the recorded candidate digest did not match the approved patch")
	}
	if applied != 0 {
		t.Fatalf("applied %d times", applied)
	}
	if !strings.Contains(stderr.String(), "container being released is not the one you approved") {
		t.Errorf("stderr = %q", stderr.String())
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
	approvePending(t, opts, "release it")
	run := runOptions(opts)
	run.Coordinate = completingCoordinator(nil)

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), run, &stdout, &stderr); code != ExitOK {
		t.Fatalf("exit: %s", stderr.String())
	}
	var result struct {
		Application string        `json:"application"`
		Lane        string        `json:"lane"`
		Weights     []int         `json:"weights"`
		State       journal.State `json:"state"`
		Outcome     string        `json:"outcome"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("piped output is not JSON: %v\n%s", err, stdout.String())
	}
	if result.Application != "payments-api" || result.Lane != "fast" {
		t.Errorf("result = %+v", result)
	}
	if len(result.Weights) != 2 || result.Weights[0] != 50 || result.Weights[1] != 100 {
		t.Errorf("weights = %v", result.Weights)
	}
	if result.State != journal.StateCompleted || result.Outcome != "released" {
		t.Errorf("result = %+v", result)
	}
}

func TestAttachedJSONRunKeepsProgressOffStdout(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := runProgressWriter(RunOptions{}, &stdout, &stderr); got != &stderr {
		t.Fatal("machine-mode progress was not routed to stderr")
	}
	terminalOut := &terminal{&stdout}
	if got := runProgressWriter(RunOptions{}, terminalOut, &stderr); got != terminalOut {
		t.Fatal("terminal progress was not kept with the readable stdout log")
	}
}

func TestRunNamesTheMissingControllerIdentityBeforeSpendingApproval(t *testing.T) {
	opts := recommended(t)
	approvePending(t, opts, "release it")
	run := runOptions(opts)
	run.Coordinate = nil

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), run, &stdout, &stderr); code == ExitOK {
		t.Fatal("run accepted a release without its controller identity")
	}
	want := config.ForApp(opts.Home, "payments-api").ForEnvironment("production").ControllerKubeconfig
	if !strings.Contains(stderr.String(), "controller identity is missing") ||
		!strings.Contains(stderr.String(), want) {
		t.Fatalf("stderr = %q, want missing identity and %q", stderr.String(), want)
	}
}
