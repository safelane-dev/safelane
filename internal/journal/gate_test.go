package journal_test

import (
	"strings"
	"testing"

	"github.com/AndrewMaged814/safelane/internal/journal"
)

// A release only widens after a genuinely new successful measurement. An
// analysis that succeeded while the previous weight was live says nothing
// about what is running now.
func TestAGateNeedsANewSuccessfulMeasurement(t *testing.T) {
	gate := journal.Gate{SuccessfulAtLastGate: 3}

	// The same reading that let the release reach this weight is not
	// permission to leave it.
	stale := gate.Decide(journal.Measurement{Phase: "Running", Successful: 3, Count: 5})
	if stale.Promote {
		t.Error("an old measurement was treated as permission to widen")
	}
	if !strings.Contains(stale.Reason, "waiting") {
		t.Errorf("reason = %q", stale.Reason)
	}

	fresh := gate.Decide(journal.Measurement{Phase: "Running", Successful: 4, Count: 6})
	if !fresh.Promote {
		t.Fatalf("a new successful measurement did not open the gate: %+v", fresh)
	}
	if fresh.By != journal.ActorSafeLane {
		t.Errorf("by = %q", fresh.By)
	}

	// After widening, the next gate needs a reading newer than that one.
	next := gate.Advance(journal.Measurement{Successful: 4})
	if next.Decide(journal.Measurement{Phase: "Running", Successful: 4, Count: 7}).Promote {
		t.Error("the gate reused the measurement that opened the previous one")
	}
}

// A missing measurement never passes. "Said nothing" and "said yes" are
// different, and the difference is the whole reason the analysis is there.
func TestAMissingMeasurementNeverPasses(t *testing.T) {
	decision := journal.Gate{}.Decide(journal.MissingMeasurement())
	if decision.Promote {
		t.Error("a release widened with no measurement at all")
	}
	if decision.Stop {
		t.Error("a missing measurement was treated as a failure; it is waiting")
	}
}

// Analysis failure ends progression, and it is attributed to Argo. SafeLane
// did not decide this, and saying "the release failed" would take credit for
// somebody else's brake.
func TestAnalysisFailureEndsProgressionAndIsAttributedToArgo(t *testing.T) {
	for _, phase := range []string{"Failed", "Error", "Inconclusive"} {
		decision := journal.Gate{}.Decide(journal.Measurement{Phase: phase, Successful: 9, Count: 10})
		if !decision.Stop {
			t.Errorf("%s did not end progression", phase)
		}
		if decision.Promote {
			t.Errorf("%s widened the release", phase)
		}
		if decision.By != journal.ActorArgo {
			t.Errorf("%s was attributed to %q", phase, decision.By)
		}
		if decision.State != journal.StateFailed {
			t.Errorf("%s left the release in %q", phase, decision.State)
		}
		if !strings.Contains(decision.Reason, strings.ToLower(phase)) {
			t.Errorf("reason = %q", decision.Reason)
		}
	}
}

// A high successful count does not rescue a failed phase: Argo has already
// decided.
func TestAFailedPhaseWinsOverASuccessfulCount(t *testing.T) {
	decision := journal.Gate{SuccessfulAtLastGate: 0}.Decide(
		journal.Measurement{Phase: "Failed", Successful: 12, Count: 12})
	if decision.Promote {
		t.Error("a failed analysis widened the release because the count had gone up")
	}
}

// The Rollout wins. SafeLane's record is a memory of what it did; the Rollout
// is what is true.
func TestTheRolloutWinsAndTheCorrectionIsReported(t *testing.T) {
	record := journal.Record{
		Application: "payments-api", Environment: "production",
		State: journal.StateMonitoring, Weight: 25,
	}

	corrected, note, changed := journal.Reconcile(record,
		journal.Observed{State: journal.StateFailed, Weight: 50}, at(5))
	if !changed {
		t.Fatal("a disagreement was not reconciled")
	}
	if corrected.State != journal.StateFailed || corrected.Weight != 50 {
		t.Errorf("record = %+v", corrected)
	}
	if note == "" || !strings.Contains(note, "the cluster says") {
		t.Errorf("the correction was not reported: %q", note)
	}
	// It is recorded as an event, so the history says the record was wrong.
	last := corrected.Events[len(corrected.Events)-1]
	if last.Kind != "reconciled" {
		t.Errorf("last event = %+v", last)
	}
}

func TestAgreementReconcilesToNothing(t *testing.T) {
	record := journal.Record{State: journal.StateMonitoring, Weight: 50}
	_, note, changed := journal.Reconcile(record,
		journal.Observed{State: journal.StateMonitoring, Weight: 50}, at(5))
	if changed || note != "" {
		t.Errorf("agreement produced a correction: %q", note)
	}
}

// A control without a recorded reason is not useful in proof.
func TestHoldContinueAndStopEachNeedAReason(t *testing.T) {
	for _, control := range []string{"hold", "continue", "stop"} {
		if err := journal.ControlReason(control, "  "); err == nil {
			t.Errorf("%s was accepted with no reason", control)
		}
		if err := journal.ControlReason(control, "waiting for the on-call handover"); err != nil {
			t.Errorf("%s with a reason was refused: %v", control, err)
		}
	}
}
