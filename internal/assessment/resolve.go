package assessment

import (
	"strings"

	"github.com/AndrewMaged814/safelane/internal/config"
	"github.com/AndrewMaged814/safelane/internal/delta"
	"github.com/AndrewMaged814/safelane/internal/release"
)

// MaxAttempts is how many times a session may submit an assessment for one
// snapshot: the first, and one correction.
//
// One correction, not three. A session that could keep resubmitting would
// eventually produce something that validates, and "eventually validated" is
// not the same claim as "was right the first time" - but it would look
// identical in the record.
const MaxAttempts = 2

// Outcome is what SafeLane does with a submitted assessment.
type Outcome struct {
	// Recommendation is what will be shown and, when proceeding, what can be
	// approved.
	Recommendation Recommendation
	// Accepted is whether the submission itself was valid.
	Accepted bool
	// Correction is set when a correction is still allowed: the submission was
	// rejected and the session may try once more.
	Correction error
	// Substituted is true when SafeLane replaced a failed assessment with a
	// waiting recommendation of its own.
	Substituted bool
}

// Resolve applies the whole rule for one submission.
//
// An `undetermined` risk and an invalid result are the same situation from
// here: the session did not produce a grounded recommendation. Either gets one
// correction attempt. If the second attempt is no better, SafeLane recommends
// waiting.
//
// It never falls back to the most cautious lane. A guarded rollout of a change
// nobody understood is still a rollout of a change nobody understood - it just
// takes longer to find out, with the same code in front of real users. Waiting
// is the honest answer, and it is the one a person can act on.
func Resolve(raw []byte, frozen delta.ReleaseDelta, policy config.Policy, attempt int) Outcome {
	recommendation, err := Parse(raw)
	if err == nil {
		err = Validate(recommendation, frozen, policy)
	}
	if err == nil && recommendation.Risk == RiskUndetermined && recommendation.Action == ActionWait {
		// Undetermined-and-waiting is a valid, honest answer. It is not a
		// failure to be corrected: the session read the evidence and says it
		// cannot tell, which is exactly what a person needs to hear.
		return Outcome{Recommendation: recommendation, Accepted: true}
	}
	if err == nil {
		return Outcome{Recommendation: recommendation, Accepted: true}
	}

	if attempt < MaxAttempts {
		return Outcome{Correction: err}
	}
	return Outcome{
		Recommendation: WaitingAfterFailure(frozen, err),
		Substituted:    true,
	}
}

// WaitingAfterFailure is the recommendation SafeLane writes for itself when the
// session could not produce a grounded one.
//
// It says so plainly rather than dressing the failure up as a finding about the
// change. "I could not assess this" and "this change is risky" are different
// statements, and only one of them is true here.
func WaitingAfterFailure(frozen delta.ReleaseDelta, reason error) Recommendation {
	return Recommendation{
		SnapshotID: frozen.SnapshotID(),
		Observations: []Observation{{
			Statement: "I read the evidence for this release but could not reach a judgement I can stand behind.",
			Evidence:  []string{"changes"},
		}},
		Risk:      RiskUndetermined,
		Action:    ActionWait,
		Rationale: "I could not assess this release, so I am not recommending it.",
		Concern: "I was not able to form a grounded view of what this change does. " +
			"That is a statement about my assessment, not about the change: it may be entirely safe.",
		Unconfirmed: "What this release changes about the running behaviour of " +
			frozen.Application() + ".",
		Blindspot: analysisBlindspot(frozen),
		NextStep: "Ask me again, or tell me what this change is meant to do and I will reassess. " +
			"If you already know it is safe, you can release it another way; nothing has been changed.",
	}
}

// analysisBlindspot says what the configured analysis measures, so a person can
// judge for themselves whether it would have caught what SafeLane could not
// reason about.
func analysisBlindspot(frozen delta.ReleaseDelta) string {
	health := frozen.Health()
	if len(health) == 0 {
		return "This Rollout has no background health analysis, so nothing would stop it automatically."
	}
	names := make([]string, 0, len(health))
	for _, objective := range health {
		names = append(names, string(objective.Name))
	}
	return "Your configured analysis (" + strings.Join(names, ", ") +
		") measures what it measures; it cannot tell you whether a change does what it was meant to do."
}

// CorrectionRequest turns a validation failure into something a session can act
// on: what was wrong, and that this is the one retry.
func CorrectionRequest(err error) string {
	var b strings.Builder
	b.WriteString("This assessment cannot be accepted as it stands. ")
	b.WriteString("Correct it and submit once more; after that SafeLane will recommend waiting.\n")
	for _, e := range release.Flatten(err) {
		b.WriteString("\n  " + e.Field + ": " + e.Message + "\n  " + e.Remedy + "\n")
	}
	return b.String()
}
