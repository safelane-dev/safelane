// Package journal is what SafeLane remembers about a release.
//
// Two shapes, for two different questions. A compact append-only card per
// release answers "what has been happening here", cheaply, ten at a time. A
// detailed directory per release answers "what exactly happened on the
// fourteenth", and is only read when somebody asks.
//
// # What is not stored
//
// No conversation, no abandoned assessment drafts, no repeated diffs, no
// ordinary tool traces, and no secret value. The last one is not enforced here
// - it is enforced at the evidence boundary, where the value was never
// captured in the first place. This package stores the frozen Delta, and the
// frozen Delta does not contain it.
//
// The rest is a decision about what a release record is *for*. A record that
// accumulated the conversation would be a record nobody reads, in a directory
// that grows without bound, containing the one thing most likely to hold
// something private.
package journal

import (
	"fmt"
	"strings"
	"time"
)

// State is where a release is. There are eight, they are named for the
// situation rather than for the code path, and the names themselves are never
// shown to a person - [Status.Line] is.
type State string

const (
	StateAssessing        State = "assessing"
	StateAwaitingApproval State = "awaiting_approval"
	StateApplying         State = "applying"
	StateMonitoring       State = "monitoring"
	StatePaused           State = "paused"
	StateCompleted        State = "completed"
	StateFailed           State = "failed"
	StateStopped          State = "stopped"
)

// States are the eight, in the order a release passes through them.
var States = []State{
	StateAssessing, StateAwaitingApproval, StateApplying, StateMonitoring,
	StatePaused, StateCompleted, StateFailed, StateStopped,
}

// Terminal reports whether a release in this state is over.
func (s State) Terminal() bool {
	switch s {
	case StateCompleted, StateFailed, StateStopped:
		return true
	}
	return false
}

// Status is a release's state plus the facts its line needs.
type Status struct {
	State State
	// Environment is where this is going, for the approval line.
	Environment string
	// Weight is the current exposure, as a percentage.
	Weight int
	// Reason is the user's own words, for a hold or a stop.
	Reason string
	// Since is when a completed release became stable.
	Since time.Time
	// Restoring means the user requested stop and Argo has not yet reported
	// the terminal restored state.
	Restoring bool
}

// Line renders Appendix A4: one sentence saying where the release is and what
// it is waiting for.
//
// The state name never appears. `monitoring` is a word about SafeLane's
// bookkeeping; "At 50%. Waiting for the next health measurement." is a word
// about the user's release, and it is the one that answers the question they
// asked.
func (s Status) Line() string {
	switch s.State {
	case StateAssessing:
		return "Reading everything changed since the running version."
	case StateAwaitingApproval:
		return fmt.Sprintf("Waiting for you to approve the rollout to %s.", s.Environment)
	case StateApplying:
		return "Applying the approved image and canary steps."
	case StateMonitoring:
		if s.Restoring {
			return fmt.Sprintf("Stopping at %d%%. Waiting for Argo to restore the stable version.", s.Weight)
		}
		return fmt.Sprintf("At %d%%. Waiting for the next health measurement.", s.Weight)
	case StatePaused:
		return fmt.Sprintf("Held at %d%% at your request: %q.", s.Weight, s.Reason)
	case StateCompleted:
		return fmt.Sprintf("Released at %d%%. Stable since %s.", orHundred(s.Weight), s.Since.UTC().Format("15:04"))
	case StateFailed:
		// Attributed to Argo, by name. SafeLane did not decide this and saying
		// "the release failed" would take credit for somebody else's brake.
		return fmt.Sprintf("Argo stopped the rollout at %d%% and restored the stable version.", s.Weight)
	case StateStopped:
		return fmt.Sprintf("Stopped at your request: %q. Stable version restored.", s.Reason)
	default:
		return "This release is in a state SafeLane does not recognise."
	}
}

// WaitingFor names what has to happen next, for a caller that wants the fact
// rather than the sentence.
func (s Status) WaitingFor() string {
	switch s.State {
	case StateAssessing:
		return "evidence"
	case StateAwaitingApproval:
		return "your approval"
	case StateApplying:
		return "the cluster to accept the change"
	case StateMonitoring:
		if s.Restoring {
			return "Argo to restore the stable version"
		}
		return "a background health measurement"
	case StatePaused:
		return "you"
	}
	return "nothing; this release is over"
}

func orHundred(weight int) int {
	if weight == 0 {
		return 100
	}
	return weight
}

// ValidState reports whether a string is one of the eight.
func ValidState(value string) bool {
	for _, state := range States {
		if string(state) == strings.TrimSpace(value) {
			return true
		}
	}
	return false
}
