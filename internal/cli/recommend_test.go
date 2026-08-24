package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/AndrewMaged814/safelane/internal/assessment"
	"github.com/AndrewMaged814/safelane/internal/config"
	"github.com/AndrewMaged814/safelane/internal/releasepatch"
	"github.com/AndrewMaged814/safelane/internal/verify/github"
)

type candidateCalls struct {
	inspectSource
	revisions    int
	defaultHeads int
}

func (s *candidateCalls) DefaultHead(ctx context.Context, repository string) (github.Revision, error) {
	s.defaultHeads++
	return s.inspectSource.DefaultHead(ctx, repository)
}

func (s *candidateCalls) Revision(ctx context.Context, repository, revision string) (github.Revision, error) {
	s.revisions++
	return s.inspectSource.Revision(ctx, repository, revision)
}

func snapshotFor(t *testing.T, opts InspectOptions) string {
	t.Helper()
	frozen, _, err := FreezeDelta(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	return frozen.SnapshotID()
}

func TestRecommendKeepsTheExactRevisionSelectedByInspect(t *testing.T) {
	opts := inspectOptions(t)
	source := &candidateCalls{inspectSource: defaultInspectSource()}
	opts.Source = source
	opts.Revision = candidateRevision

	var inspected, inspectErr bytes.Buffer
	if code := Inspect(context.Background(), opts, &inspected, &inspectErr); code != ExitOK {
		t.Fatalf("inspect: %s", inspectErr.String())
	}
	var result struct {
		Snapshot string `json:"snapshot_id"`
	}
	if err := json.Unmarshal(inspected.Bytes(), &result); err != nil {
		t.Fatal(err)
	}

	// The next command has no revision argument by design; it must recover
	// the exact inspected candidate from the pending inspection.
	opts.Revision = ""
	var stdout, stderr bytes.Buffer
	if code := Recommend(context.Background(), RecommendOptions{
		Inspect: opts, AssessmentPath: "-", Stdin: bytes.NewReader(a2Assessment(t, result.Snapshot)),
	}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("recommend: %s", stderr.String())
	}
	if source.revisions != 2 || source.defaultHeads != 0 {
		t.Fatalf("revision reads=%d default-head reads=%d", source.revisions, source.defaultHeads)
	}
}

// a2Assessment is Appendix A2's example: three observations, low risk, and the
// fast lane the configured mapping points at.
func a2Assessment(t *testing.T, snapshot string) []byte {
	t.Helper()
	raw, err := json.Marshal(assessment.Recommendation{
		SnapshotID: snapshot,
		Observations: []assessment.Observation{
			{Statement: "The version you want to release passed its required build and test checks.", Evidence: []string{"changes"}},
			{Statement: "The container prepared for deployment came from that successful build.", Evidence: []string{"deployment"}},
			{Statement: "I reviewed everything changed since the currently running version and found nothing requiring additional release precautions.", Evidence: []string{"changes"}},
		},
		Risk:      assessment.RiskLow,
		Action:    assessment.ActionProceed,
		Lane:      "fast",
		Rationale: "Nothing in this change needs extra release precautions.",
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// a3Assessment is Appendix A3's example. The prose is carried verbatim,
// linebreaks included, because the wording of a waiting recommendation is the
// session's and the sections around it are the contract's.
func a3Assessment(t *testing.T, snapshot string) []byte {
	t.Helper()
	raw, err := json.Marshal(assessment.Recommendation{
		SnapshotID: snapshot,
		Observations: []assessment.Observation{
			{Statement: "The change alters how payments-api writes to its database.", Evidence: []string{"changes"}},
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
		Concern: "This change alters how payments-api writes to its database, and the schema migration runs\n" +
			"during start-up of the new version. If it fails part-way, the stable version cannot read\n" +
			"the rows that were already migrated.",
		Unconfirmed: "Whether the migration has been run against production-sized data.",
		Blindspot: "Your configured analysis measures request success rate over two minutes. A migration that\n" +
			"completes but leaves rows unreadable would not appear there.",
		NextStep: "Run the migration as a separate job before this release, or tell me it already ran and I\n" +
			"will reassess.",
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// A proceeding recommendation prints A2 and then A5, copied out of the plan.
func TestProceedingPrintsA2ThenA5(t *testing.T) {
	opts := inspectOptions(t)
	// A2's example is the fast lane, which the configured mapping gives low
	// risk.
	raw := a2Assessment(t, snapshotFor(t, opts))

	var stdout, stderr bytes.Buffer
	code := Recommend(context.Background(), RecommendOptions{
		Inspect: opts, AssessmentPath: "-", Stdin: bytes.NewReader(raw),
	}, &terminal{&stdout}, &stderr)
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	assertGolden(t, "a2-a5-proceeding.txt", stdout.String())
}

func TestWaitingPrintsA3(t *testing.T) {
	opts := inspectOptions(t)
	raw := a3Assessment(t, snapshotFor(t, opts))

	var stdout, stderr bytes.Buffer
	code := Recommend(context.Background(), RecommendOptions{
		Inspect: opts, AssessmentPath: "-", Stdin: bytes.NewReader(raw),
	}, &terminal{&stdout}, &stderr)
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	assertGolden(t, "a3-waiting.txt", stdout.String())
}

// A person deciding whether to ship something is not helped by being handed
// the filing system.
func TestTheRecommendationShowsNoRiskLabelsIdentifiersOrCommands(t *testing.T) {
	opts := inspectOptions(t)
	for name, raw := range map[string][]byte{
		"proceeding": a2Assessment(t, snapshotFor(t, opts)),
		"waiting":    a3Assessment(t, snapshotFor(t, opts)),
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Recommend(context.Background(), RecommendOptions{
				Inspect: opts, AssessmentPath: "-", Stdin: bytes.NewReader(raw),
			}, &terminal{&stdout}, &stderr); code != ExitOK {
				t.Fatalf("exit: %s", stderr.String())
			}
			out := stdout.String()
			// Risk labels, identifiers, hashes, schema versions and commands.
			// Not the words "hazard" or "schema" in ordinary prose - a person
			// describing a migration is allowed to say "schema".
			for _, forbidden := range []string{
				"sha256:", "snapshot", "rel_", "schema_version",
				"Low risk", "Medium risk", "High risk", "undetermined",
				"safelane ", "kubectl ",
			} {
				if strings.Contains(out, forbidden) {
					t.Errorf("the recommendation showed %q:\n%s", forbidden, out)
				}
			}
		})
	}
}

// The rollout sentence follows the configured lane. Two weights read exactly
// as the appendix does; more weights add a stop each, in the same shape.
func TestTheProposedRolloutFollowsTheConfiguredLane(t *testing.T) {
	for lane, want := range map[string]string{
		"fast":     "Release to 50%, verify the configured health analysis, then continue to 100%.",
		"standard": "Release to 25%, verify the configured health analysis, then 50%, verify again, then continue to 100%.",
		"guarded":  "Release to 25%, verify the configured health analysis, then 50%, verify again, then 75%, verify again, then continue to 100%.",
	} {
		t.Run(lane, func(t *testing.T) {
			risk := map[string]assessment.Risk{
				"fast": assessment.RiskLow, "standard": assessment.RiskMedium, "guarded": assessment.RiskHigh,
			}[lane]

			opts := inspectOptions(t)
			r := assessment.Recommendation{
				SnapshotID:   snapshotFor(t, opts),
				Observations: []assessment.Observation{{Statement: "I read the change.", Evidence: []string{"changes"}}},
				Risk:         risk, Action: assessment.ActionProceed, Lane: lane,
				Rationale: "It is what it is.",
			}
			raw, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}

			var stdout, stderr bytes.Buffer
			if code := Recommend(context.Background(), RecommendOptions{
				Inspect: opts, AssessmentPath: "-", Stdin: bytes.NewReader(raw),
			}, &terminal{&stdout}, &stderr); code != ExitOK {
				t.Fatalf("exit: %s", stderr.String())
			}
			if !strings.Contains(stdout.String(), want) {
				t.Errorf("rollout sentence:\n%s\nwant %q", stdout.String(), want)
			}
			if !strings.Contains(stdout.String(), "Using your "+lane+" lane.") {
				t.Errorf("the lane was not named:\n%s", stdout.String())
			}
		})
	}
}

// An invalid assessment gets one correction attempt, and the request says what
// was wrong.
func TestAnInvalidAssessmentAsksForOneCorrection(t *testing.T) {
	opts := inspectOptions(t)
	broken := []byte(`{"snapshot":"sha256:wrong","observations":[],"risk":"low","action":"proceed","lane":"fast","rationale":"x"}`)

	var stdout, stderr bytes.Buffer
	code := Recommend(context.Background(), RecommendOptions{
		Inspect: opts, AssessmentPath: "-", Stdin: bytes.NewReader(broken), Attempt: 1,
	}, &terminal{&stdout}, &stderr)
	if code != ExitDecision {
		t.Fatalf("exit %d, want a correction request", code)
	}
	if !strings.Contains(stderr.String(), "submit once more") {
		t.Errorf("stderr = %q", stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "" {
		t.Errorf("a rejected assessment produced a recommendation:\n%s", stdout.String())
	}
}

// After the correction, SafeLane recommends waiting. It never falls back to
// the most cautious lane: a guarded rollout of a change nobody understood is
// still a rollout of a change nobody understood.
func TestASecondInvalidAssessmentBecomesAWaitingRecommendation(t *testing.T) {
	opts := inspectOptions(t)
	broken := []byte(`{"snapshot":"sha256:wrong","observations":[],"risk":"low","action":"proceed","lane":"fast","rationale":"x"}`)

	var stdout, stderr bytes.Buffer
	code := Recommend(context.Background(), RecommendOptions{
		Inspect: opts, AssessmentPath: "-", Stdin: bytes.NewReader(broken),
		Attempt: assessment.MaxAttempts,
	}, &terminal{&stdout}, &stderr)
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.HasPrefix(out, "I recommend waiting on this release.") {
		t.Errorf("output:\n%s", out)
	}
	if strings.Contains(out, "guarded") {
		t.Errorf("SafeLane fell back to a lane instead of waiting:\n%s", out)
	}
	if !strings.Contains(out, "not about the change") {
		t.Errorf("the substitution should say it could not assess:\n%s", out)
	}
}

// The machine form carries the structured recommendation and the same text.
func TestRecommendIsJSONWhenPiped(t *testing.T) {
	opts := inspectOptions(t)
	raw := a2Assessment(t, snapshotFor(t, opts))

	var stdout, stderr bytes.Buffer
	if code := Recommend(context.Background(), RecommendOptions{
		Inspect: opts, AssessmentPath: "-", Stdin: bytes.NewReader(raw),
	}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("exit: %s", stderr.String())
	}
	var result struct {
		SnapshotID     string                    `json:"snapshot_id"`
		Recommendation assessment.Recommendation `json:"recommendation"`
		Text           string                    `json:"text"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("piped output is not JSON: %v\n%s", err, stdout.String())
	}
	if result.Recommendation.Lane != "fast" {
		t.Errorf("lane = %q", result.Recommendation.Lane)
	}
	if !strings.Contains(result.Text, "Proceed with this rollout to production?") {
		t.Errorf("text = %q", result.Text)
	}
	// The recommendation is about the snapshot that carries the lane it chose,
	// so the evidence and the proposal agree from here on.
	if result.Recommendation.SnapshotID != result.SnapshotID {
		t.Errorf("recommendation describes %s but the snapshot is %s",
			result.Recommendation.SnapshotID, result.SnapshotID)
	}
}

func TestRecommendFreezesUserProvidedEvidenceIntoTheFinalSnapshot(t *testing.T) {
	opts := inspectOptions(t)
	snapshot := snapshotFor(t, opts)
	var recommendation assessment.Recommendation
	if err := json.Unmarshal(a2Assessment(t, snapshot), &recommendation); err != nil {
		t.Fatal(err)
	}
	recommendation.Provided = []assessment.ProvidedEvidence{{
		Kind: "deployment fact", Value: "the migration already ran", Source: "user confirmation",
		At: "2026-08-21T12:05:00Z", Candidate: candidateRevision, Environment: "production",
	}}
	raw, _ := json.Marshal(recommendation)
	var stdout, stderr bytes.Buffer
	if code := Recommend(context.Background(), RecommendOptions{
		Inspect: opts, AssessmentPath: "-", Stdin: bytes.NewReader(raw),
	}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("recommend: %s", stderr.String())
	}
	pending, found, err := releasepatch.LoadPending(config.ForApp(opts.Home, "payments-api").ForEnvironment("production").Dir)
	if err != nil || !found {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	var stored struct {
		Provided []struct {
			Candidate   string `json:"candidate"`
			Environment string `json:"environment"`
		} `json:"provided"`
	}
	if err := json.Unmarshal(pending.Delta, &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Provided) != 1 || stored.Provided[0].Candidate != candidateRevision || stored.Provided[0].Environment != "production" {
		t.Fatalf("provided evidence was not frozen: %+v", stored.Provided)
	}
}

// Environment impact informs the explanation and never selects a lane. The
// same assessment against a low-impact environment produces the same lane.
func TestEnvironmentImpactDoesNotSelectTheLane(t *testing.T) {
	for _, impact := range []config.Impact{config.ImpactLow, config.ImpactCritical} {
		opts := inspectOptions(t)
		rewriteImpact(t, opts.Home, impact)

		raw := a2Assessment(t, snapshotFor(t, opts))
		var stdout, stderr bytes.Buffer
		if code := Recommend(context.Background(), RecommendOptions{
			Inspect: opts, AssessmentPath: "-", Stdin: bytes.NewReader(raw),
		}, &terminal{&stdout}, &stderr); code != ExitOK {
			t.Fatalf("exit (%s impact): %s", impact, stderr.String())
		}
		if !strings.Contains(stdout.String(), "Using your fast lane.") {
			t.Errorf("%s impact changed the lane:\n%s", impact, stdout.String())
		}
	}
}

func rewriteImpact(t *testing.T, home string, impact config.Impact) {
	t.Helper()
	file := config.Render(config.Discovered{
		Application: config.Application{Name: "payments-api", Repository: "acme/payments-api"},
		Artifact:    config.Artifact{Container: "payments-api", Image: "ghcr.io/acme/payments-api"},
		Environment: config.Environment{
			Name: "production", Impact: impact,
			Kubernetes: config.Kubernetes{
				Context: "safelane-caller-payments-api", Namespace: "payments", Rollout: "payments-api",
			},
		},
	}, config.DefaultReleaseSettings())
	if _, err := config.Write(config.ForApp(home, "payments-api").File, file); err != nil {
		t.Fatal(err)
	}
}
