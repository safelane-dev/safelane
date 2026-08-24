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
)

func at4(minute int) time.Time {
	return time.Date(2026, 8, 21, 14, minute, 0, 0, time.UTC)
}

// Appendix A4 fixes one line per state, and the state name itself is never
// shown. This renders all eight from the values the appendix uses.
func TestStatusLinesMatchA4(t *testing.T) {
	statuses := []journal.Status{
		{State: journal.StateAssessing},
		{State: journal.StateAwaitingApproval, Environment: "production"},
		{State: journal.StateApplying},
		{State: journal.StateMonitoring, Weight: 50},
		{State: journal.StatePaused, Weight: 25, Reason: "waiting for the on-call handover"},
		{State: journal.StateCompleted, Weight: 100, Since: time.Date(2026, 8, 21, 14, 12, 0, 0, time.UTC)},
		{State: journal.StateFailed, Weight: 50},
		{State: journal.StateStopped, Reason: "customer reported errors"},
	}

	var b strings.Builder
	for i, status := range statuses {
		// The golden is the appendix's own two-column table. The left column
		// is the state name, which is the half a person never sees.
		b.WriteString(padState(string(journal.States[i])) + status.Line() + "\n")
	}
	assertGolden(t, "a4-status-lines.txt", b.String())
}

func padState(state string) string {
	const width = len("awaiting_approval") + 2
	return state + strings.Repeat(" ", width-len(state))
}

// Every state says what it is waiting for, and no line shows the state name.
func TestEveryStateSaysWhatItIsWaitingFor(t *testing.T) {
	for _, state := range journal.States {
		status := journal.Status{State: state, Environment: "production"}
		if status.WaitingFor() == "" {
			t.Errorf("%s does not say what it is waiting for", state)
		}
		if state.Terminal() && !strings.Contains(status.WaitingFor(), "over") {
			t.Errorf("%s is terminal but claims to be waiting for %q", state, status.WaitingFor())
		}
		if strings.Contains(status.Line(), string(state)) {
			t.Errorf("the status line for %s shows the state name: %q", state, status.Line())
		}
	}
}

func controlOptions(t *testing.T) (ControlOptions, journal.Store) {
	t.Helper()
	home := registeredHome(t)
	dir := config.ForApp(home, "payments-api").ForEnvironment("production").Dir
	store := journal.Store{Dir: dir}

	if _, err := store.Start(journal.Record{
		ID:          journal.NewID("payments-api", "production", 1, at4(0)),
		Application: "payments-api", Environment: "production",
		Candidate: strings.Repeat("a", 40), Lane: "standard",
		State: journal.StateMonitoring, Weight: 25, Started: at4(0),
	}); err != nil {
		t.Fatal(err)
	}

	return ControlOptions{
		Root: ".", Home: home, Environment: "production",
		Origin: func(string) (string, error) { return "acme/payments-api", nil },
		Now:    func() time.Time { return at4(5) },
		// Both ports are stated, not left nil. Nil means the real cluster, and
		// a test that quietly reached one would be testing the network.
		Observe: func(context.Context, config.Environment) (journal.Observed, error) {
			return journal.Observed{State: journal.StateMonitoring, Weight: 25}, nil
		},
		Control: func(context.Context, string, config.Environment) error { return nil },
	}, store
}

// Nothing takes an identifier: the pair resolves the release.
func TestTheControlsResolveTheActiveReleaseFromThePair(t *testing.T) {
	opts, _ := controlOptions(t)
	var stdout, stderr bytes.Buffer
	if code := Status(context.Background(), opts, &terminal{&stdout}, &stderr); code != ExitOK {
		t.Fatalf("status: %s", stderr.String())
	}
	if !strings.Contains(stdout.String(), "At 25%. Waiting for the next health measurement.") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

func TestTheControlsSayWhenThereIsNoActiveRelease(t *testing.T) {
	opts := ControlOptions{
		Root: ".", Home: registeredHome(t), Environment: "production",
		Origin: func(string) (string, error) { return "acme/payments-api", nil },
	}
	var stdout, stderr bytes.Buffer
	if code := Status(context.Background(), opts, &stdout, &stderr); code == ExitOK {
		t.Fatal("status reported on a release that is not happening")
	}
	if !strings.Contains(stderr.String(), "no release of payments-api to production in progress") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

// The Rollout wins. SafeLane reconciles, says so, and never asks a person to
// choose which to believe.
func TestWhenTheRecordAndTheClusterDisagreeTheRolloutWins(t *testing.T) {
	opts, store := controlOptions(t)
	opts.Observe = func(context.Context, config.Environment) (journal.Observed, error) {
		return journal.Observed{State: journal.StateFailed, Weight: 50, Aborted: true}, nil
	}

	var stdout, stderr bytes.Buffer
	if code := Status(context.Background(), opts, &terminal{&stdout}, &stderr); code != ExitOK {
		t.Fatalf("status: %s", stderr.String())
	}
	if !strings.Contains(stdout.String(), "the cluster says") {
		t.Errorf("the correction was not reported:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Argo stopped the rollout at 50%") {
		t.Errorf("stdout = %q", stdout.String())
	}

	record, _, err := store.Active()
	if err != nil {
		t.Fatal(err)
	}
	if record.State != journal.StateFailed || record.Weight != 50 {
		t.Errorf("the record was not reconciled: %+v", record)
	}
}

// Hold stays where it is. A person who wanted the stable version back would
// have said stop.
func TestHoldPausesWithoutWideningExposure(t *testing.T) {
	opts, store := controlOptions(t)
	opts.Reason = "waiting for the on-call handover"
	asked := ""
	opts.Control = func(_ context.Context, action string, _ config.Environment) error {
		asked = action
		return nil
	}
	opts.Observe = nil // hold does not read; it asks.

	var stdout, stderr bytes.Buffer
	if code := Hold(context.Background(), opts, &terminal{&stdout}, &stderr); code != ExitOK {
		t.Fatalf("hold: %s", stderr.String())
	}
	if asked != "hold" {
		t.Errorf("argo was asked to %q", asked)
	}
	record, _, err := store.Active()
	if err != nil {
		t.Fatal(err)
	}
	if record.State != journal.StatePaused {
		t.Errorf("state = %q", record.State)
	}
	if record.Weight != 25 {
		t.Errorf("hold changed the exposure to %d%%", record.Weight)
	}
	if !strings.Contains(stdout.String(), "Held at 25% at your request:") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

func TestContinueRequiresExplicitIntent(t *testing.T) {
	opts, store := controlOptions(t)
	opts.Reason = "the handover is done"

	var stdout, stderr bytes.Buffer
	if code := Continue(context.Background(), opts, &terminal{&stdout}, &stderr); code != ExitOK {
		t.Fatalf("continue: %s", stderr.String())
	}
	record, _, err := store.Active()
	if err != nil {
		t.Fatal(err)
	}
	if record.State != journal.StateMonitoring {
		t.Errorf("state = %q", record.State)
	}
	last := record.Events[len(record.Events)-1]
	if last.Kind != "continue" || !strings.Contains(last.Detail, "the handover is done") {
		t.Errorf("last event = %+v", last)
	}
}

func TestStopAbortsAndRecordsTheReason(t *testing.T) {
	opts, store := controlOptions(t)
	opts.Reason = "customer reported errors"
	asked := ""
	opts.Control = func(_ context.Context, action string, _ config.Environment) error {
		asked = action
		return nil
	}
	opts.Observe = nil // stop does not read; it asks.

	var stdout, stderr bytes.Buffer
	if code := Stop(context.Background(), opts, &terminal{&stdout}, &stderr); code != ExitOK {
		t.Fatalf("stop: %s", stderr.String())
	}
	if asked != "stop" {
		t.Errorf("argo was asked to %q", asked)
	}
	if !strings.Contains(stdout.String(), "Waiting for Argo to restore the stable version.") {
		t.Errorf("stdout = %q", stdout.String())
	}

	// The request is not terminal proof. The release stays active until Argo
	// reports that restoration has actually finished.
	if active, found, err := store.Active(); err != nil || !found || active.State != journal.StateMonitoring {
		t.Errorf("Active = %+v %v %v after a stop request", active, found, err)
	}
	opts.Observe = func(context.Context, config.Environment) (journal.Observed, error) {
		return journal.Observed{State: journal.StateFailed, Weight: 0}, nil
	}
	stdout.Reset()
	if code := Status(context.Background(), opts, &terminal{&stdout}, &stderr); code != ExitOK {
		t.Fatalf("status after restore: %s", stderr.String())
	}
	if !strings.Contains(stdout.String(), "Stable version restored.") {
		t.Errorf("terminal stdout = %q", stdout.String())
	}
	if _, found, err := store.Active(); err != nil || found {
		t.Errorf("Active = %v %v after restoration", found, err)
	}
	cards, err := store.History(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 || cards[0].Reason != "customer reported errors" {
		t.Errorf("history = %+v", cards)
	}
}

// A control without a recorded reason is not useful in proof.
func TestHoldContinueAndStopRefuseWithNoReason(t *testing.T) {
	for name, run := range map[string]func(context.Context, ControlOptions, *bytes.Buffer, *bytes.Buffer) int{
		"hold": func(c context.Context, o ControlOptions, out, errs *bytes.Buffer) int { return Hold(c, o, out, errs) },
		"continue": func(c context.Context, o ControlOptions, out, errs *bytes.Buffer) int {
			return Continue(c, o, out, errs)
		},
		"stop": func(c context.Context, o ControlOptions, out, errs *bytes.Buffer) int { return Stop(c, o, out, errs) },
	} {
		t.Run(name, func(t *testing.T) {
			opts, _ := controlOptions(t)
			var stdout, stderr bytes.Buffer
			if code := run(context.Background(), opts, &stdout, &stderr); code == ExitOK {
				t.Fatalf("%s was accepted with no reason", name)
			}
			if !strings.Contains(stderr.String(), "needs a reason") {
				t.Errorf("stderr = %q", stderr.String())
			}
		})
	}
}

// Proof is compact by default. Loading full proof every time would spend an
// agent's context on records nobody asked for.
func TestProofIsCompactUnlessDetailsAreAsked(t *testing.T) {
	opts, store := controlOptions(t)
	record, _, err := store.Active()
	if err != nil {
		t.Fatal(err)
	}
	record.Delta = json.RawMessage(`{"snapshot_id":"sha256:abc"}`)
	if err := store.Save(record); err != nil {
		t.Fatal(err)
	}

	var compact, stderr bytes.Buffer
	if code := Proof(context.Background(), opts, &compact, &stderr); code != ExitOK {
		t.Fatalf("proof: %s", stderr.String())
	}
	if strings.Contains(compact.String(), "snapshot_id") {
		t.Errorf("compact proof carried the frozen evidence:\n%s", compact.String())
	}

	opts.Details = true
	var detailed bytes.Buffer
	if code := Proof(context.Background(), opts, &detailed, &stderr); code != ExitOK {
		t.Fatalf("proof --details: %s", stderr.String())
	}
	if !strings.Contains(detailed.String(), "snapshot_id") {
		t.Errorf("detailed proof did not carry the frozen evidence:\n%s", detailed.String())
	}
}

func TestDetailedProofRemainsAvailableAfterCompletion(t *testing.T) {
	opts, store := controlOptions(t)
	record, _, err := store.Active()
	if err != nil {
		t.Fatal(err)
	}
	record.Delta = json.RawMessage(`{"snapshot_id":"sha256:complete"}`)
	if _, err := store.Finish(record, journal.StateCompleted, "released at 100%", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	opts.Details = true
	var stdout, stderr bytes.Buffer
	if code := Proof(context.Background(), opts, &stdout, &stderr); code != ExitOK {
		t.Fatalf("proof --details after completion: %s", stderr.String())
	}
	if !strings.Contains(stdout.String(), "sha256:complete") || !strings.Contains(stdout.String(), "completed") {
		t.Fatalf("terminal proof is incomplete: %s", stdout.String())
	}
}

func TestStatusIsJSONWhenPiped(t *testing.T) {
	opts, _ := controlOptions(t)
	var stdout, stderr bytes.Buffer
	if code := Status(context.Background(), opts, &stdout, &stderr); code != ExitOK {
		t.Fatalf("status: %s", stderr.String())
	}
	var result struct {
		State      string `json:"state"`
		WaitingFor string `json:"waiting_for"`
		Line       string `json:"line"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("piped output is not JSON: %v\n%s", err, stdout.String())
	}
	if result.State != "monitoring" || result.WaitingFor != "a background health measurement" {
		t.Errorf("result = %+v", result)
	}
}
