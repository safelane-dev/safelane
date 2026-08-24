package journal

import (
	"fmt"
	"strings"
	"time"

	"github.com/AndrewMaged814/safelane/internal/release"
)

// Measurement is one reading from the application's own background analysis.
//
// SafeLane does not compute these and cannot influence them. It reads what
// Argo recorded and decides only whether to ask for the next promotion.
type Measurement struct {
	// Phase is Argo's word: Running, Successful, Failed, Error, Inconclusive.
	Phase string
	// Successful is how many measurements have succeeded so far. It is a
	// running count, which is what makes "a new one" checkable.
	Successful int
	// Count is how many have been taken, successful or not.
	Count int
	// Measured is the last value, for saying what was seen.
	Measured float64
}

// Gate decides whether a release may widen.
//
// The rule is one sentence and the whole point of the design: a release only
// moves on after the application's own analysis has produced a *new* successful
// measurement since the last time it moved.
//
// "New" is doing the work. An analysis that succeeded before the canary
// existed has succeeded; an analysis whose last reading was taken while the
// previous weight was live has succeeded. Neither of them says anything about
// what is running now, and treating either as permission would let a release
// walk from 25% to 100% on one old reading.
type Gate struct {
	// SuccessfulAtLastGate is the successful-measurement count when the
	// release last widened.
	SuccessfulAtLastGate int
}

// Decision is what the gate decided, and why - in words that name Argo when
// Argo is the one who decided.
type Decision struct {
	// Promote is whether SafeLane should ask Argo to move on.
	Promote bool
	// Stop is whether progression has ended. Argo decided this, not SafeLane.
	Stop bool
	// State is what the release is now, when progression ended.
	State State
	// Reason is a sentence for the record.
	Reason string
	// By is who decided.
	By Actor
}

// Decide applies the rule to one reading.
func (g Gate) Decide(m Measurement) Decision {
	switch m.Phase {
	case "Failed", "Error", "Inconclusive":
		// Argo's call. SafeLane records what happened and stops asking for
		// promotions, but the coordinator remains attached until the Rollout
		// reports that restoration reached a terminal outcome.
		return Decision{
			Stop:   true,
			State:  StateFailed,
			By:     ActorArgo,
			Reason: fmt.Sprintf("the background analysis reported %s", strings.ToLower(m.Phase)),
		}
	}

	if m.Successful > g.SuccessfulAtLastGate {
		return Decision{
			Promote: true,
			By:      ActorSafeLane,
			Reason:  fmt.Sprintf("a new successful measurement (%d of %d)", m.Successful, m.Count),
		}
	}

	// No new measurement is not a pass. It is not a failure either - it is
	// waiting, and waiting is the correct thing to do.
	return Decision{
		By:     ActorSafeLane,
		Reason: "waiting for the next health measurement",
	}
}

// Advance records that the release widened, so the next gate needs a reading
// newer than this one.
func (g Gate) Advance(m Measurement) Gate {
	g.SuccessfulAtLastGate = m.Successful
	return g
}

// MissingMeasurement is what a reading that never arrived amounts to.
//
// It never passes. An analysis with no measurements is an analysis that has
// not said the canary is healthy, and the difference between "said nothing"
// and "said yes" is the whole reason the analysis is there.
func MissingMeasurement() Measurement { return Measurement{Phase: "Running"} }

// Observed is what the cluster says about a release right now.
type Observed struct {
	State  State
	Weight int
	// AtGate distinguishes an indefinite canary pause from ordinary
	// progression. Measurements may authorize promotion only at a gate.
	AtGate bool
	// Aborted is Argo's own abort, as opposed to a stop SafeLane asked for.
	Aborted bool
}

// Reconcile makes a stored record agree with the cluster.
//
// The Rollout wins. SafeLane's record is a memory of what it did; the Rollout
// is what is true. When they disagree the record is wrong, and asking a person
// to choose between them would be asking them to do SafeLane's job.
//
// The correction is reported rather than applied silently: a release that
// quietly changed state between two `status` calls would be a release nobody
// could reason about.
func Reconcile(record Record, observed Observed, now time.Time) (Record, string, bool) {
	if record.State == observed.State && record.Weight == observed.Weight {
		return record, "", false
	}

	note := fmt.Sprintf("SafeLane had this release as %s; the cluster says %s.",
		record.Status().Line(), Status{
			State: observed.State, Environment: record.Environment,
			Weight: observed.Weight, Reason: record.Reason,
		}.Line())

	record.State = observed.State
	record.Weight = observed.Weight
	record.Events = append(record.Events, Event{
		At: now, Kind: "reconciled", By: ActorSafeLane,
		Detail: note, Weight: observed.Weight,
	})
	return record, note, true
}

// ControlReason refuses a hold, continue or stop with nothing said.
//
// A control without a recorded reason is not useful in proof, and "somebody
// paused it" six weeks later is the same as not knowing.
func ControlReason(control, reason string) error {
	if strings.TrimSpace(reason) != "" {
		return nil
	}
	return release.Invalid("missing_reason", "reason",
		fmt.Sprintf("%s needs a reason", control),
		"Say why, in your own words; it goes into the record.")
}
