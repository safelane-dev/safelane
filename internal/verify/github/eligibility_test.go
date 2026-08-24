package github_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/AndrewMaged814/safelane/internal/release"
	"github.com/AndrewMaged814/safelane/internal/verify/github"
	"github.com/AndrewMaged814/safelane/internal/verify/oci"
)

func eligible() github.EligibilityInput {
	return github.EligibilityInput{
		Repository: "acme/payments-api",
		Candidate: github.Candidate{
			Revision: github.Revision{SHA: headSHA, Subject: "feat: add refunds", OnDefaultBranch: true},
		},
		Deployed: github.Revision{SHA: deployedSHA, OnDefaultBranch: true},
		Artifact: oci.Artifact{
			Repository: "ghcr.io/acme/payments-api",
			Digest:     "sha256:" + strings.Repeat("1", 64),
		},
		ArtifactSource: oci.SourceMetadata{
			Source:   "https://github.com/acme/payments-api",
			Revision: headSHA,
			Method:   oci.BindingOCILabels,
		},
		Protection: github.Repository{
			FullName: "acme/payments-api", DefaultBranch: "main",
			Protected: true, RequiredChecks: []string{"build-and-push"},
		},
		Checks: github.Checks{
			Revision: headSHA,
			Runs: []github.CheckRun{
				{Name: "build-and-push", Status: "completed", Conclusion: "success", HeadSHA: headSHA},
			},
			Workflows: []github.WorkflowRun{
				{ID: 42, Name: "build-and-push", Status: "completed", Conclusion: "success", HeadSHA: headSHA},
			},
		},
		Comparison:          github.Comparison{Base: deployedSHA, Head: headSHA, Status: "ahead", AheadBy: 3},
		ConfirmedWorkflowID: 42,
	}
}

func TestAFullyEvidencedReleaseIsEligible(t *testing.T) {
	result := github.EvaluateEligibility(eligible())
	if !result.Eligible {
		t.Fatalf("not eligible: %s", blockerCodes(result))
	}
	if result.Rejection() != nil {
		t.Errorf("an eligible release produced a rejection: %v", result.Rejection())
	}
}

// The gate speaks eligibility, never risk. "Your build failed" is not a risk
// level, and dressing it up as one would produce a release that goes out
// slowly instead of a release that does not go out.
func TestBlockersAreEvidenceNotRisk(t *testing.T) {
	in := eligible()
	in.Checks.Runs[0].Conclusion = "failure"

	result := github.EvaluateEligibility(in)
	assertBlocker(t, result, "required_check_failed")

	err := result.Rejection()
	if !errors.Is(err, release.ErrEvidenceFailed) {
		t.Errorf("an eligibility failure must be evidence, got %v", err)
	}
	for _, blocker := range result.Blockers {
		for _, word := range []string{"risk", "hazard", "lane"} {
			if strings.Contains(strings.ToLower(blocker.Reason+blocker.Remedy), word) {
				t.Errorf("an eligibility blocker used the word %q: %+v", word, blocker)
			}
		}
	}
}

// Pending and failed lead a person to do different things, so they are
// different blockers.
func TestPendingChecksAreNotFailedChecks(t *testing.T) {
	in := eligible()
	in.Checks.Runs[0].Status = "in_progress"
	in.Checks.Runs[0].Conclusion = ""

	result := github.EvaluateEligibility(in)
	blocker := assertBlocker(t, result, "required_check_pending")
	if !strings.Contains(blocker.Remedy, "Wait") {
		t.Errorf("a pending check should say to wait: %q", blocker.Remedy)
	}
	assertNoBlocker(t, result, "required_check_failed")
}

func TestARequiredCheckThatNeverReportedIsBlocked(t *testing.T) {
	in := eligible()
	in.Checks.Runs = nil

	assertBlocker(t, github.EvaluateEligibility(in), "required_check_missing")
}

// A check run reported against some other commit is somebody else's evidence.
func TestChecksAreScopedToTheExactRevision(t *testing.T) {
	in := eligible()
	in.ArtifactSource.Method = oci.BindingCIProvenance
	in.Checks = github.Checks{Revision: headSHA, Runs: []github.CheckRun{
		{Name: "build-and-push", Status: "completed", Conclusion: "success", HeadSHA: olderSHA},
	}}
	// The gate believes what it is handed; the client filters. This asserts
	// the client's filtering separately - see TestChecksIgnoreOtherRevisions.
	if result := github.EvaluateEligibility(in); !result.Eligible {
		t.Fatalf("unexpected blockers: %s", blockerCodes(result))
	}
}

func TestAnUntraceableContainerStopsBeforeAssessment(t *testing.T) {
	in := eligible()
	in.ArtifactSource = oci.SourceMetadata{}

	blocker := assertBlocker(t, github.EvaluateEligibility(in), "untraceable_artifact")
	if !strings.Contains(blocker.Reason, "built from") {
		t.Errorf("reason = %q", blocker.Reason)
	}
}

func TestAContainerBuiltFromAnotherRevisionIsRefused(t *testing.T) {
	in := eligible()
	in.ArtifactSource.Revision = olderSHA

	assertBlocker(t, github.EvaluateEligibility(in), "artifact_is_a_different_revision")
}

func TestAContainerBuiltFromAnotherRepositoryIsRefused(t *testing.T) {
	in := eligible()
	in.ArtifactSource.Source = "https://github.com/acme/orders-api"

	assertBlocker(t, github.EvaluateEligibility(in), "artifact_is_a_different_repository")
}

func TestNoResolvedContainerIsBlocked(t *testing.T) {
	in := eligible()
	in.Artifact = oci.Artifact{}

	assertBlocker(t, github.EvaluateEligibility(in), "no_artifact")
}

// SafeLane will not roll a release backwards, and it will not quietly ship
// something unrelated.
func TestACandidateOlderThanWhatIsRunningIsRefused(t *testing.T) {
	in := eligible()
	in.Comparison.Status = "behind"

	blocker := assertBlocker(t, github.EvaluateEligibility(in), "candidate_older_than_deployed")
	if !strings.Contains(blocker.Remedy, "backwards") {
		t.Errorf("remedy = %q", blocker.Remedy)
	}
}

func TestUnrelatedHistoriesAreRefused(t *testing.T) {
	in := eligible()
	in.Comparison.Status = "diverged"

	assertBlocker(t, github.EvaluateEligibility(in), "unrelated_histories")
}

func TestReleasingWhatIsAlreadyRunningIsRefused(t *testing.T) {
	in := eligible()
	in.Comparison.Status = "identical"

	assertBlocker(t, github.EvaluateEligibility(in), "candidate_already_deployed")
}

func TestAnUnknownDeployedRevisionIsBlocked(t *testing.T) {
	in := eligible()
	in.Deployed = github.Revision{}

	blocker := assertBlocker(t, github.EvaluateEligibility(in), "no_deployed_revision")
	if !strings.Contains(blocker.Remedy, "Confirm the deployed commit") {
		t.Errorf("remedy = %q", blocker.Remedy)
	}
	// Ordering says nothing when there is nothing to order against.
	assertNoBlocker(t, github.EvaluateEligibility(in), "candidate_older_than_deployed")
}

// Missing branch protection is reported either way: it does not stop a
// release, and staying quiet about it would be the wrong kind of helpful.
func TestMissingBranchProtectionIsReportedNotBlocked(t *testing.T) {
	in := eligible()
	in.Protection = github.Repository{FullName: "acme/payments-api", DefaultBranch: "main"}
	in.Checks.Workflows = []github.WorkflowRun{
		{ID: 42, Name: "build-and-push", Status: "completed", Conclusion: "success", HeadSHA: headSHA},
	}
	in.ConfirmedWorkflowID = 42

	result := github.EvaluateEligibility(in)
	if !result.Eligible {
		t.Fatalf("unexpected blockers: %s", blockerCodes(result))
	}
	if len(result.Notices) == 0 || !strings.Contains(result.Notices[0], "no branch protection") {
		t.Errorf("notices = %v", result.Notices)
	}
}

// A protected branch that requires nothing is also reported, and it is a
// different sentence, because it is a different situation.
func TestAProtectedBranchWithNoRequiredChecksIsReported(t *testing.T) {
	in := eligible()
	in.Protection.RequiredChecks = nil
	in.Checks.Workflows = []github.WorkflowRun{
		{ID: 42, Name: "build-and-push", Status: "completed", Conclusion: "success", HeadSHA: headSHA},
	}
	in.ConfirmedWorkflowID = 42

	result := github.EvaluateEligibility(in)
	if !result.Eligible {
		t.Fatalf("unexpected blockers: %s", blockerCodes(result))
	}
	if len(result.Notices) == 0 || !strings.Contains(result.Notices[0], "requires no status checks") {
		t.Errorf("notices = %v", result.Notices)
	}
}

// With no required checks, provenance that names the producing run answers the
// question and nobody is asked anything.
func TestProvenanceIdentifiesTheProducingRunWithoutAsking(t *testing.T) {
	in := eligible()
	in.Protection = github.Repository{FullName: "acme/payments-api", DefaultBranch: "main"}
	in.ArtifactSource.Method = oci.BindingCIProvenance

	result := github.EvaluateEligibility(in)
	if !result.Eligible {
		t.Fatalf("unexpected blockers: %s", blockerCodes(result))
	}
}

func TestOneSuccessfulRunStillNeedsReleaseScopedConfirmation(t *testing.T) {
	in := eligible()
	in.Protection = github.Repository{FullName: "acme/payments-api", DefaultBranch: "main"}
	in.Checks.Workflows = []github.WorkflowRun{
		{ID: 42, Name: "build-and-push", Status: "completed", Conclusion: "success", HeadSHA: headSHA},
	}

	in.ConfirmedWorkflowID = 0
	assertBlocker(t, github.EvaluateEligibility(in), "build_provenance_ambiguous")
	in.ConfirmedWorkflowID = 42
	if result := github.EvaluateEligibility(in); !result.Eligible {
		t.Fatalf("the confirmed exact-revision run remained blocked: %s", blockerCodes(result))
	}
}

// Without provenance, multiple successful runs remain ambiguous.
func TestWithoutProvenanceMultipleRunsRequireBetterProvenance(t *testing.T) {
	in := eligible()
	in.Protection = github.Repository{FullName: "acme/payments-api", DefaultBranch: "main"}
	in.Checks.Workflows = []github.WorkflowRun{
		{ID: 41, Name: "build-and-push", Status: "completed", Conclusion: "success", HeadSHA: headSHA},
		{ID: 42, Name: "publish", Status: "completed", Conclusion: "success", HeadSHA: headSHA},
		{ID: 43, Name: "lint", Status: "completed", Conclusion: "failure", HeadSHA: headSHA},
	}
	in.ConfirmedWorkflowID = 0

	blocker := assertBlocker(t, github.EvaluateEligibility(in), "build_provenance_ambiguous")
	if !strings.Contains(blocker.Remedy, "run 41") || !strings.Contains(blocker.Remedy, "run 42") {
		t.Errorf("the question should list the candidates: %q", blocker.Remedy)
	}
	if strings.Contains(blocker.Remedy, "run 43") {
		t.Errorf("a failed run was offered as a candidate: %q", blocker.Remedy)
	}

}

func TestAListedWorkflowCanBeConfirmedForThisRelease(t *testing.T) {
	in := eligible()
	in.Protection = github.Repository{FullName: "acme/payments-api", DefaultBranch: "main"}
	in.Checks.Workflows = []github.WorkflowRun{
		{ID: 41, Name: "build-and-push", Status: "completed", Conclusion: "success", HeadSHA: headSHA},
		{ID: 42, Name: "publish", Status: "completed", Conclusion: "success", HeadSHA: headSHA},
	}
	in.ConfirmedWorkflowID = 42
	if result := github.EvaluateEligibility(in); !result.Eligible {
		t.Fatalf("confirmed successful workflow remained ambiguous: %s", blockerCodes(result))
	}
}

func TestNoSuccessfulBuildIsBlocked(t *testing.T) {
	in := eligible()
	in.Protection = github.Repository{FullName: "acme/payments-api", DefaultBranch: "main"}
	in.Checks.Workflows = []github.WorkflowRun{
		{ID: 41, Name: "build-and-push", Status: "completed", Conclusion: "failure", HeadSHA: headSHA},
	}
	in.ConfirmedWorkflowID = 0

	assertBlocker(t, github.EvaluateEligibility(in), "no_successful_build")
}

// Everything wrong at once is reported at once. A person would rather learn
// both now than one per attempt.
func TestEveryBlockerIsReportedTogether(t *testing.T) {
	in := eligible()
	in.Checks.Runs[0].Conclusion = "failure"
	in.ArtifactSource = oci.SourceMetadata{}
	in.Deployed = github.Revision{}

	result := github.EvaluateEligibility(in)
	for _, code := range []string{"required_check_failed", "untraceable_artifact", "no_deployed_revision"} {
		assertBlocker(t, result, code)
	}
}
