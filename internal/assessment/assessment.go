// Package assessment validates the judgement and renders the sentence a person
// reads.
//
// # SafeLane never runs a model
//
// There is no client here, no prompt, no API key, and no subprocess. The
// assessment is produced by the Claude session the user is already talking to,
// and arrives here as one structured value to be checked. That is a structural
// property, not a policy: this package imports nothing that could reach a model,
// and a test asserts it.
//
// The point is what it makes true elsewhere. Because production code cannot
// spawn an assessor, every test of every downstream behaviour - lane selection,
// approval, execution - runs against a fixed assessment value with no network,
// no non-determinism, and no cost. A release path that could call a model
// somewhere in the middle would be a release path nobody could test.
//
// # What the validator checks, and what it does not
//
// It checks structure and grounding: that the result describes the snapshot it
// was given, that every observation cites evidence that exists, that every
// hazard states what has to be true for it to happen and what happens then,
// that coverage is one of four honest states with an explanation, and that a
// proceeding result names a lane the operator configured and the one the risk
// mapping points at.
//
// It does not check whether the judgement is right. There are no path rules, no
// size thresholds, and no authorship penalties - those were deterministic
// heuristics wearing a semantic costume, and they produced confident answers
// about changes they had not read. Being unable to grade the reasoning is the
// honest position; refusing ungrounded reasoning is what this package can
// actually do.
package assessment

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/AndrewMaged814/safelane/internal/config"
	"github.com/AndrewMaged814/safelane/internal/delta"
	"github.com/AndrewMaged814/safelane/internal/release"
)

// Risk is the assessment's own judgement of how much could go wrong.
type Risk string

const (
	RiskLow    Risk = "low"
	RiskMedium Risk = "medium"
	RiskHigh   Risk = "high"
	// RiskUndetermined is a real answer: the session could not reach a
	// grounded judgement. It is never quietly turned into "high" and never
	// resolves to a lane.
	RiskUndetermined Risk = "undetermined"
)

// Action is what the assessment recommends. There are two, and there is no
// third: an assessment cannot half-approve, defer, or escalate.
type Action string

const (
	ActionProceed Action = "proceed"
	ActionWait    Action = "wait"
)

// CoverageStatus is how well the application's configured health analysis
// would catch a hazard if it happened.
//
// Four states, and `unknown` is one of them on purpose. Collapsing "I cannot
// tell" into "not covered" would make the assessment look more certain than it
// is, in the direction that sounds responsible - which is the direction people
// stop reading.
type CoverageStatus string

const (
	CoverageCovered   CoverageStatus = "covered"
	CoveragePartial   CoverageStatus = "partially_covered"
	CoverageNone      CoverageStatus = "not_covered"
	CoverageUnknown   CoverageStatus = "unknown"
	coverageStatusSet                = "covered, partially_covered, not_covered, unknown"
)

// Recommendation is the whole of what the session hands back.
type Recommendation struct {
	// SnapshotID is the Delta this describes. A recommendation about a
	// different snapshot is a recommendation about a different release.
	SnapshotID   string        `json:"snapshot"`
	Observations []Observation `json:"observations"`
	Hazards      []Hazard      `json:"hazards"`
	// HistoryFindings are patterns from previous releases of this pair.
	HistoryFindings []HistoryFinding `json:"history_findings,omitempty"`
	Risk            Risk             `json:"risk"`
	Action          Action           `json:"action"`
	// Lane is set only when proceeding, and must be the lane the operator's
	// risk mapping points at.
	Lane string `json:"lane,omitempty"`
	// Rationale is the plain-language reason, in release language.
	Rationale string `json:"rationale"`
	// NextStep is what a person should do about a waiting recommendation.
	// Required when waiting: A3 ends with one, and "wait" with no next step is
	// a dead end rather than advice.
	NextStep string `json:"next_step,omitempty"`
	// Concern, Unconfirmed, and Blindspot are the three things A3 says before
	// the next step. They are separate fields because they answer separate
	// questions and a single blob would let one of them go missing.
	Concern     string             `json:"concern,omitempty"`
	Unconfirmed string             `json:"unconfirmed,omitempty"`
	Blindspot   string             `json:"analysis_blindspot,omitempty"`
	Provided    []ProvidedEvidence `json:"provided_evidence,omitempty"`
}

// Observation is one thing the session noticed, and where it saw it.
type Observation struct {
	Statement string `json:"statement"`
	// Evidence is where this came from: a view name, or an evidence handle.
	// An observation with no citation is an opinion, and opinions do not ship.
	Evidence []string `json:"evidence"`
}

// Hazard is one way this release could hurt somebody.
type Hazard struct {
	Name string `json:"name"`
	// Evidence is what makes this a hazard rather than a worry.
	Evidence []string `json:"evidence"`
	// Preconditions are what has to be true for it to actually happen. A
	// hazard with no preconditions is a mood.
	Preconditions []string `json:"preconditions"`
	// Consequence is what a person or a system experiences if it does.
	Consequence string `json:"consequence"`
	// Coverage is whether the configured analysis would notice.
	Coverage Coverage `json:"coverage"`
}

// Coverage is what the application's own health analysis would and would not
// catch.
type Coverage struct {
	Status      CoverageStatus `json:"status"`
	Evidence    []string       `json:"evidence,omitempty"`
	Explanation string         `json:"explanation"`
}

// HistoryFinding is a pattern from this Application and Environment's past.
type HistoryFinding struct {
	Statement string   `json:"statement"`
	Evidence  []string `json:"evidence"`
}

// ProvidedEvidence is a fact the user supplied during the assessment. It is
// attached to the frozen snapshot with its source and time. Conversation text,
// drafts and tool traces are not.
type ProvidedEvidence struct {
	Kind   string `json:"kind"`
	Value  string `json:"value"`
	Source string `json:"source"`
	At     string `json:"at"`
}

// Parse decodes a submitted assessment.
//
// Unknown fields are refused. A result carrying a field the contract does not
// have is a result written against a different contract, and the half of it
// that happens to fit is not a reason to accept the whole.
func Parse(raw []byte) (Recommendation, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var recommendation Recommendation
	if err := decoder.Decode(&recommendation); err != nil {
		return Recommendation{}, release.Malformed("invalid_assessment", "assessment",
			fmt.Sprintf("the assessment is not readable: %v", err),
			"Return only the fields the assessment contract defines.")
	}
	return recommendation, nil
}

// Validate checks structure and grounding against the frozen evidence and the
// operator's configuration.
//
// It reports every problem at once, because a correction attempt that fixes one
// thing and discovers the next is not a correction attempt, it is a queue.
func Validate(r Recommendation, frozen delta.ReleaseDelta, policy config.Policy) error {
	var errs release.Errors

	if r.SnapshotID != frozen.SnapshotID() {
		errs = append(errs, release.Invalid("wrong_snapshot", "snapshot",
			"this assessment describes a different set of evidence",
			"Assess the snapshot you were given, or ask for the current one."))
	}

	known := knownEvidence(frozen)
	errs = append(errs, validateObservations(r, known)...)
	errs = append(errs, validateHazards(r, known)...)
	errs = append(errs, validateHistoryFindings(r, known)...)
	errs = append(errs, validateDecision(r, policy)...)

	if strings.TrimSpace(r.Rationale) == "" {
		errs = append(errs, missing("rationale", "the assessment gave no reason",
			"Say, in one plain sentence, why this is the recommendation."))
	}
	return errs.OrNil()
}

// knownEvidence is everything the assessment could legitimately have looked
// at: the four views that are always there, and the handles for what loads on
// demand.
//
// Citing anything else is not a small mistake. A cited handle that does not
// exist means the reasoning was not grounded in this release's evidence, and
// there is no way to tell from the outside what it was grounded in instead.
func knownEvidence(frozen delta.ReleaseDelta) map[string]bool {
	known := make(map[string]bool, len(delta.ViewNames)+4)
	for _, name := range delta.ViewNames {
		known[name] = true
	}
	for _, handle := range frozen.Handles() {
		known[handle.ID] = true
	}
	return known
}

func validateObservations(r Recommendation, known map[string]bool) release.Errors {
	var errs release.Errors
	if len(r.Observations) == 0 {
		errs = append(errs, missing("observations", "the assessment observed nothing",
			"State at least one thing you read, and where you read it."))
	}
	for i, observation := range r.Observations {
		field := fmt.Sprintf("observations[%d]", i)
		if strings.TrimSpace(observation.Statement) == "" {
			errs = append(errs, missing(field+".statement", "an observation says nothing",
				"State what you observed."))
		}
		errs = append(errs, citations(field+".evidence", observation.Evidence, known,
			"an observation cited no evidence")...)
	}
	return errs
}

func validateHazards(r Recommendation, known map[string]bool) release.Errors {
	var errs release.Errors
	for i, hazard := range r.Hazards {
		field := fmt.Sprintf("hazards[%d]", i)
		if strings.TrimSpace(hazard.Name) == "" {
			errs = append(errs, missing(field+".name", "a hazard has no name",
				"Name the hazard."))
		}
		errs = append(errs, citations(field+".evidence", hazard.Evidence, known,
			"a hazard cited no evidence")...)

		// The two causal fields. Without them a hazard is a mood: something
		// that sounds worrying, cannot be checked, and cannot be designed
		// around.
		if len(hazard.Preconditions) == 0 {
			errs = append(errs, missing(field+".preconditions",
				fmt.Sprintf("hazard %q does not say what has to be true for it to happen", hazard.Name),
				"State the preconditions, so somebody can tell whether they hold."))
		}
		if strings.TrimSpace(hazard.Consequence) == "" {
			errs = append(errs, missing(field+".consequence",
				fmt.Sprintf("hazard %q does not say what happens if it does", hazard.Name),
				"State the consequence in terms of what a person or a system experiences."))
		}

		switch hazard.Coverage.Status {
		case CoverageCovered, CoveragePartial, CoverageNone, CoverageUnknown:
		default:
			errs = append(errs, release.Invalid("invalid_coverage", field+".coverage.status",
				fmt.Sprintf("%q is not a coverage state", hazard.Coverage.Status),
				"Use one of: "+coverageStatusSet+"."))
		}
		if strings.TrimSpace(hazard.Coverage.Explanation) == "" {
			errs = append(errs, missing(field+".coverage.explanation",
				fmt.Sprintf("hazard %q does not say what the configured analysis would do about it", hazard.Name),
				"Explain what the analysis would and would not catch."))
		}
		errs = append(errs, unknownHandles(field+".coverage.evidence", hazard.Coverage.Evidence, known)...)
	}
	return errs
}

func validateHistoryFindings(r Recommendation, known map[string]bool) release.Errors {
	var errs release.Errors
	for i, finding := range r.HistoryFindings {
		field := fmt.Sprintf("history_findings[%d]", i)
		if strings.TrimSpace(finding.Statement) == "" {
			errs = append(errs, missing(field+".statement", "a history finding says nothing",
				"State what the history shows."))
		}
		errs = append(errs, citations(field+".evidence", finding.Evidence, known,
			"a history finding cited no evidence")...)
	}
	return errs
}

// validateDecision checks the two-value action, the risk vocabulary, and the
// one rule that keeps lane selection with the operator: a proceeding
// recommendation may only name the lane the configured risk mapping points at.
//
// The assessment judges risk. The operator decides what each risk level is
// worth in traffic. Letting a recommendation name any lane it liked would move
// that decision into the assessment, quietly, one release at a time.
func validateDecision(r Recommendation, policy config.Policy) release.Errors {
	var errs release.Errors

	switch r.Risk {
	case RiskLow, RiskMedium, RiskHigh, RiskUndetermined:
	default:
		errs = append(errs, release.Invalid("invalid_risk", "risk",
			fmt.Sprintf("%q is not a risk level", r.Risk),
			"Use one of: low, medium, high, undetermined."))
	}

	switch r.Action {
	case ActionProceed:
		errs = append(errs, validateProceeding(r, policy)...)
	case ActionWait:
		errs = append(errs, validateWaiting(r)...)
	default:
		errs = append(errs, release.Invalid("invalid_action", "action",
			fmt.Sprintf("%q is not an action", r.Action),
			"Recommend one of: proceed, wait."))
	}
	return errs
}

func validateProceeding(r Recommendation, policy config.Policy) release.Errors {
	var errs release.Errors
	if r.Risk == RiskUndetermined {
		errs = append(errs, release.Invalid("undetermined_cannot_proceed", "action",
			"an undetermined assessment cannot recommend proceeding",
			"Recommend waiting, or reach a grounded judgement."))
		return errs
	}
	if strings.TrimSpace(r.Lane) == "" {
		errs = append(errs, missing("lane", "a proceeding recommendation named no lane",
			"Name the configured lane for this risk level."))
		return errs
	}
	if _, declared := policy.Lanes[r.Lane]; !declared {
		errs = append(errs, release.Invalid("undeclared_lane", "lane",
			fmt.Sprintf("%q is not a lane this application has configured", r.Lane),
			"Name one of: "+strings.Join(policy.LaneNames(), ", ")+"."))
		return errs
	}
	if want := policy.RiskMapping[config.Risk(r.Risk)]; want != "" && want != r.Lane {
		errs = append(errs, release.Invalid("lane_does_not_match_risk", "lane",
			fmt.Sprintf("%s risk is configured to use the %s lane, not %s", r.Risk, want, r.Lane),
			"Use the configured lane for the risk you assessed, or assess a different risk."))
	}
	return errs
}

func validateWaiting(r Recommendation) release.Errors {
	var errs release.Errors
	// A waiting recommendation carries no lane, because there is nothing to
	// run. A lane here would be a proposal wearing a refusal's clothes.
	if strings.TrimSpace(r.Lane) != "" {
		errs = append(errs, release.Invalid("waiting_named_a_lane", "lane",
			"a waiting recommendation named a lane",
			"Leave the lane out; there is nothing to run yet."))
	}
	if strings.TrimSpace(r.NextStep) == "" {
		errs = append(errs, missing("next_step", "a waiting recommendation gave no next step",
			"Say what would let this release go ahead."))
	}
	return errs
}

func citations(field string, evidence []string, known map[string]bool, empty string) release.Errors {
	var errs release.Errors
	if len(evidence) == 0 {
		errs = append(errs, missing(field, empty,
			"Cite the view or the evidence handle you read it in."))
		return errs
	}
	return unknownHandles(field, evidence, known)
}

func unknownHandles(field string, evidence []string, known map[string]bool) release.Errors {
	var errs release.Errors
	for i, handle := range evidence {
		if known[handle] {
			continue
		}
		errs = append(errs, release.Invalid("unknown_evidence", fmt.Sprintf("%s[%d]", field, i),
			fmt.Sprintf("%q is not part of this release's evidence", handle),
			"Cite one of: "+strings.Join(sortedKeys(known), ", ")+"."))
	}
	return errs
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func missing(field, message, remedy string) *release.Error {
	return release.Invalid("incomplete_assessment", field, message, remedy)
}
