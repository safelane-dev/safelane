package assessment_test

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AndrewMaged814/safelane/internal/assessment"
	"github.com/AndrewMaged814/safelane/internal/config"
	"github.com/AndrewMaged814/safelane/internal/delta"
	"github.com/AndrewMaged814/safelane/internal/release"
)

func frozen(t *testing.T) delta.ReleaseDelta {
	t.Helper()
	analysisBody := delta.NewHandle("analysis", []byte(`{"metrics":[]}`), "success-rate structure")
	return delta.Freeze(delta.Input{
		Application: "payments-api",
		Environment: "production",
		Candidate: delta.ArtifactBinding{
			Digest: "sha256:" + strings.Repeat("a", 64), Revision: strings.Repeat("a", 40),
		},
		Changes: delta.ChangeSet{
			Base: strings.Repeat("d", 40), Head: strings.Repeat("a", 40), Status: "ahead",
			Commits: []delta.Commit{{SHA: strings.Repeat("a", 40), Subject: "feat: add refunds"}},
		},
		Deployment: delta.DeploymentEvidence{Environment: "production", Impact: "critical"},
		Health: []delta.HealthObjective{{
			Name: "success-rate", Provider: "Prometheus", Resolved: true, Body: &analysisBody,
		}},
		CapturedAt: time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC),
	})
}

func proceeding(t *testing.T) assessment.Recommendation {
	t.Helper()
	return assessment.Recommendation{
		SnapshotID: frozen(t).SnapshotID(),
		Observations: []assessment.Observation{
			{Statement: "The version you want to release passed its required build and test checks.", Evidence: []string{"changes"}},
			{Statement: "The container prepared for deployment came from that successful build.", Evidence: []string{"deployment"}},
		},
		Risk:      assessment.RiskLow,
		Action:    assessment.ActionProceed,
		Lane:      "fast",
		Rationale: "Nothing in the change needs extra release precautions.",
	}
}

func waiting(t *testing.T) assessment.Recommendation {
	t.Helper()
	return assessment.Recommendation{
		SnapshotID: frozen(t).SnapshotID(),
		Observations: []assessment.Observation{
			{Statement: "The change alters how the service writes to its database.", Evidence: []string{"changes"}},
		},
		Hazards: []assessment.Hazard{{
			Name:          "partial schema migration",
			Evidence:      []string{"changes"},
			Preconditions: []string{"the migration runs during start-up", "it fails part-way"},
			Consequence:   "The stable version cannot read the rows that were already migrated.",
			Coverage: assessment.Coverage{
				Status:      assessment.CoverageNone,
				Explanation: "Request success rate would not show unreadable rows.",
			},
		}},
		Risk:      assessment.RiskHigh,
		Action:    assessment.ActionWait,
		Rationale: "The migration's blast radius is not bounded by the configured analysis.",
		NextStep:  "Run the migration as a separate job before this release.",
	}
}

func releaseSettings() config.ReleaseSettings { return config.DefaultReleaseSettings() }

func TestAWellGroundedProceedingResultIsAccepted(t *testing.T) {
	if err := assessment.Validate(proceeding(t), frozen(t), releaseSettings()); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestUserProvidedEvidenceIsBoundToThisCandidateAndEnvironment(t *testing.T) {
	f := frozen(t)
	r := proceeding(t)
	r.Provided = []assessment.ProvidedEvidence{{
		Kind: "deployment fact", Value: "the migration already ran", Source: "user confirmation",
		At: "2026-08-21T12:05:00Z", Candidate: f.Candidate().Revision, Environment: f.Environment(),
	}}
	if err := assessment.Validate(r, f, releaseSettings()); err != nil {
		t.Fatalf("valid provided evidence: %v", err)
	}
	r.Provided[0].Candidate = strings.Repeat("b", 40)
	assertRejection(t, assessment.Validate(r, f, releaseSettings()), "wrong_evidence_candidate")
}

func TestAWellGroundedWaitingResultIsAccepted(t *testing.T) {
	if err := assessment.Validate(waiting(t), frozen(t), releaseSettings()); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// A recommendation about a different snapshot is a recommendation about a
// different release.
func TestAnAssessmentOfAnotherSnapshotIsRefused(t *testing.T) {
	r := proceeding(t)
	r.SnapshotID = "sha256:" + strings.Repeat("f", 64)
	assertRejection(t, assessment.Validate(r, frozen(t), releaseSettings()), "wrong_snapshot")
}

// An observation with no citation is an opinion, and opinions do not ship.
func TestAnObservationMustCiteEvidence(t *testing.T) {
	r := proceeding(t)
	r.Observations[0].Evidence = nil
	assertRejection(t, assessment.Validate(r, frozen(t), releaseSettings()), "incomplete_assessment")
}

// A cited handle that does not exist means the reasoning was not grounded in
// this release's evidence, and there is no way to tell what it was grounded in
// instead.
func TestCitingEvidenceThatDoesNotExistIsRefused(t *testing.T) {
	r := proceeding(t)
	r.Observations[0].Evidence = []string{"diff:sha256:" + strings.Repeat("0", 64)}
	err := assessment.Validate(r, frozen(t), releaseSettings())
	assertRejection(t, err, "unknown_evidence")
	// The refusal lists what could have been cited.
	assertRemedy(t, err, "changes")
}

func TestTheFourViewsAndTheDeltasHandlesAreValidCitations(t *testing.T) {
	f := frozen(t)
	r := proceeding(t)
	r.Observations[0].Evidence = append([]string{}, delta.ViewNames...)
	r.Observations = append(r.Observations, assessment.Observation{
		Statement: "The diff adds one function.",
		Evidence:  []string{f.Handles()[0].ID},
	})
	if err := assessment.Validate(r, f, releaseSettings()); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// A hazard with no preconditions is a mood: it sounds worrying, cannot be
// checked, and cannot be designed around.
func TestAHazardMustSayWhatHasToBeTrueAndWhatHappens(t *testing.T) {
	for name, mutate := range map[string]func(*assessment.Hazard){
		"no preconditions": func(h *assessment.Hazard) { h.Preconditions = nil },
		"no consequence":   func(h *assessment.Hazard) { h.Consequence = "" },
		"no coverage":      func(h *assessment.Hazard) { h.Coverage.Explanation = "" },
	} {
		t.Run(name, func(t *testing.T) {
			r := waiting(t)
			mutate(&r.Hazards[0])
			assertRejection(t, assessment.Validate(r, frozen(t), releaseSettings()), "incomplete_assessment")
		})
	}
}

// Four coverage states, and "unknown" is one of them. Collapsing "I cannot
// tell" into "not covered" would make the assessment look more certain than it
// is, in the direction people stop reading.
func TestTheFourCoverageStatesAreAccepted(t *testing.T) {
	for _, status := range []assessment.CoverageStatus{
		assessment.CoverageCovered, assessment.CoveragePartial,
		assessment.CoverageNone, assessment.CoverageUnknown,
	} {
		r := waiting(t)
		r.Hazards[0].Coverage.Status = status
		if err := assessment.Validate(r, frozen(t), releaseSettings()); err != nil {
			t.Errorf("coverage %q was refused: %v", status, err)
		}
	}

	r := waiting(t)
	r.Hazards[0].Coverage.Status = "probably_fine"
	assertRejection(t, assessment.Validate(r, frozen(t), releaseSettings()), "invalid_coverage")
}

// The assessment judges risk; configured Release Settings decide what each risk level is
// worth in traffic. A recommendation that could name any lane would move that
// decision, quietly, one release at a time.
func TestAProceedingResultMustUseTheConfiguredLaneForItsRisk(t *testing.T) {
	r := proceeding(t)
	r.Risk = assessment.RiskHigh // configured to guarded
	r.Lane = "fast"
	err := assessment.Validate(r, frozen(t), releaseSettings())
	assertRejection(t, err, "lane_does_not_match_risk")
	assertRemedy(t, err, "configured lane")
}

func TestAProceedingResultMustNameALaneThatExists(t *testing.T) {
	r := proceeding(t)
	r.Lane = "instant"
	assertRejection(t, assessment.Validate(r, frozen(t), releaseSettings()), "undeclared_lane")
}

func TestAProceedingResultMustNameALane(t *testing.T) {
	r := proceeding(t)
	r.Lane = ""
	assertRejection(t, assessment.Validate(r, frozen(t), releaseSettings()), "incomplete_assessment")
}

// A lane on a waiting recommendation would be a proposal wearing a refusal's
// clothes.
func TestAWaitingResultCarriesNoLane(t *testing.T) {
	r := waiting(t)
	r.Lane = "guarded"
	assertRejection(t, assessment.Validate(r, frozen(t), releaseSettings()), "waiting_named_a_lane")
}

func TestAWaitingResultMustSayWhatToDoNext(t *testing.T) {
	r := waiting(t)
	r.NextStep = ""
	assertRejection(t, assessment.Validate(r, frozen(t), releaseSettings()), "incomplete_assessment")
}

// Two actions, and no third. An assessment cannot half-approve, defer, or
// escalate.
func TestOnlyProceedAndWaitAreActions(t *testing.T) {
	for _, action := range []assessment.Action{"escalate", "approve", "defer", ""} {
		r := proceeding(t)
		r.Action = action
		assertRejection(t, assessment.Validate(r, frozen(t), releaseSettings()), "invalid_action")
	}
}

func TestUndeterminedCannotProceed(t *testing.T) {
	r := proceeding(t)
	r.Risk = assessment.RiskUndetermined
	assertRejection(t, assessment.Validate(r, frozen(t), releaseSettings()), "undetermined_cannot_proceed")
}

// Undetermined-and-waiting is an honest answer, not a failure to be corrected.
func TestUndeterminedAndWaitingIsAccepted(t *testing.T) {
	r := waiting(t)
	r.Risk = assessment.RiskUndetermined
	if err := assessment.Validate(r, frozen(t), releaseSettings()); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	raw, _ := json.Marshal(r)
	outcome := assessment.Resolve(raw, frozen(t), releaseSettings(), 1)
	if !outcome.Accepted || outcome.Substituted {
		t.Errorf("outcome = %+v", outcome)
	}
}

// A result carrying a field the contract does not have was written against a
// different contract, and the half that fits is not a reason to accept it.
func TestAnUnknownFieldIsRefused(t *testing.T) {
	_, err := assessment.Parse([]byte(`{"snapshot":"x","approved":true}`))
	assertRejection(t, err, "invalid_assessment")
}

// One correction, then SafeLane recommends waiting. Never a silent fall back
// to the most cautious lane.
func TestAnInvalidResultGetsOneCorrectionThenWaits(t *testing.T) {
	f := frozen(t)
	broken := []byte(`{"snapshot":"sha256:nope","observations":[],"risk":"low","action":"proceed","lane":"fast","rationale":"x"}`)

	first := assessment.Resolve(broken, f, releaseSettings(), 1)
	if first.Accepted || first.Substituted || first.Correction == nil {
		t.Fatalf("first attempt = %+v", first)
	}
	if !strings.Contains(assessment.CorrectionRequest(first.Correction), "submit once more") {
		t.Errorf("correction request = %q", assessment.CorrectionRequest(first.Correction))
	}

	second := assessment.Resolve(broken, f, releaseSettings(), assessment.MaxAttempts)
	if second.Accepted || !second.Substituted {
		t.Fatalf("second attempt = %+v", second)
	}
	if second.Recommendation.Action != assessment.ActionWait {
		t.Errorf("action = %q, want wait", second.Recommendation.Action)
	}
	if second.Recommendation.Lane != "" {
		t.Errorf("SafeLane fell back to the %q lane instead of waiting", second.Recommendation.Lane)
	}
	// It says it could not assess, rather than claiming the change is risky.
	if !strings.Contains(second.Recommendation.Concern, "not about the change") {
		t.Errorf("concern = %q", second.Recommendation.Concern)
	}
}

func TestAValidThirdSubmissionCannotReopenAnExhaustedSnapshot(t *testing.T) {
	f := frozen(t)
	valid := proceeding(t)
	valid.SnapshotID = f.SnapshotID()
	raw, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}

	outcome := assessment.Resolve(raw, f, releaseSettings(), assessment.MaxAttempts+1)
	if outcome.Accepted || !outcome.Substituted || outcome.Recommendation.Action != assessment.ActionWait {
		t.Fatalf("third attempt = %+v", outcome)
	}
}

// Environment impact is context for the explanation. It never adds risk and it
// never selects a lane.
func TestEnvironmentImpactDoesNotChangeTheDecision(t *testing.T) {
	base := frozen(t)
	r := proceeding(t)

	if err := assessment.Validate(r, base, releaseSettings()); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	// The same assessment against a low-impact environment validates
	// identically: nothing in this package reads impact at all.
	low := delta.Freeze(delta.Input{
		Application: "payments-api", Environment: "production",
		Deployment: delta.DeploymentEvidence{Environment: "production", Impact: "low"},
		Changes:    base.Changes(), Candidate: base.Candidate(),
		Health: base.Health(), CapturedAt: base.CapturedAt(),
	})
	r.SnapshotID = low.SnapshotID()
	if err := assessment.Validate(r, low, releaseSettings()); err != nil {
		t.Fatalf("impact changed the outcome: %v", err)
	}
}

// SafeLane never runs a model, and this is structural rather than a rule
// somebody has to remember: the package imports nothing that could reach one.
func TestNothingHereCanSpawnAModel(t *testing.T) {
	forbidden := []string{"os/exec", "net/http", "net/rpc"}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), filepath.Join(".", entry.Name()), nil, parser.ImportsOnly)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		for _, imported := range file.Imports {
			path := strings.Trim(imported.Path.Value, `"`)
			for _, banned := range forbidden {
				if path == banned {
					t.Errorf("%s imports %s; SafeLane must not be able to run an assessor", entry.Name(), path)
				}
			}
		}
	}
}

func assertRejection(t *testing.T, err error, code string) {
	t.Helper()
	for _, e := range release.Flatten(err) {
		if e.Code == code {
			return
		}
	}
	t.Errorf("want a rejection with code %q, got %v", code, err)
}

func assertRemedy(t *testing.T, err error, want string) {
	t.Helper()
	for _, e := range release.Flatten(err) {
		if strings.Contains(e.Remedy, want) {
			return
		}
	}
	t.Errorf("want a remedy containing %q, got %v", want, err)
}
