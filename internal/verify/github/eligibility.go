package github

import (
	"fmt"
	"sort"
	"strings"

	"github.com/AndrewMaged814/safelane/internal/release"
	"github.com/AndrewMaged814/safelane/internal/verify/oci"
)

// Eligibility is the gate that runs before anything decides how risky a change
// is.
//
// The two questions are not the same question, and collapsing them is the
// mistake this type exists to prevent. "Your build failed" is not a risk
// level. "SafeLane cannot tell what is running" is not a risk level. Dressing
// either of them up as high risk would produce a release that goes out slowly
// instead of a release that does not go out - and would teach a person to read
// "high risk" as "SafeLane is confused", which is exactly the wrong lesson to
// learn about a tool that also says "high risk" when it means it.
type Eligibility struct {
	// Eligible is whether the release may proceed to assessment at all.
	Eligible bool `json:"eligible"`
	// Blockers are why not. Each one is phrased as eligibility.
	Blockers []Blocker `json:"blockers,omitempty"`
	// Notices are things worth reporting that do not stop a release - most
	// often that the default branch requires no checks at all.
	Notices []string `json:"notices,omitempty"`
	// ConfirmedRun is the workflow run a person confirmed produced the
	// container, when one was needed.
	ConfirmedRun int64 `json:"confirmed_run,omitempty"`
}

// Blocker is one reason a release is not eligible.
type Blocker struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
	Remedy string `json:"remedy"`
}

// EligibilityInput is everything already observed. Nothing here is fetched:
// the gate is a pure function of facts somebody else gathered, so the rules
// can be read in one place and tested without a network.
type EligibilityInput struct {
	// Repository is the registered repository, as owner/name.
	Repository string
	// Candidate is the exact revision being released.
	Candidate Candidate
	// Deployed is the exact revision currently running. An empty SHA means
	// SafeLane could not establish one, which is itself a blocker.
	Deployed Revision
	// Artifact is the container selected for the candidate.
	Artifact oci.Artifact
	// ArtifactSource is the proved binding from that container to a revision.
	// A zero value means the container is untraceable.
	ArtifactSource oci.SourceMetadata
	// Protection is the default branch's protection, for required checks.
	Protection Repository
	// Checks is what CI reported for the candidate revision.
	Checks Checks
	// Comparison is deployed...candidate, for the ordering and relatedness
	// rules.
	Comparison Comparison
	// ConfirmedRun is the workflow run the user confirmed produced the
	// container, when branch protection required no checks and provenance
	// could not identify one. It is release-scoped evidence, never
	// configuration: the next release asks again, because the next release is
	// a different build.
	ConfirmedRun int64
}

// EvaluateEligibility applies every rule at once and reports all of them.
//
// All of them, not the first: a person whose build failed and whose branch has
// no protection would rather learn both now than one per attempt.
func EvaluateEligibility(in EligibilityInput) Eligibility {
	result := Eligibility{ConfirmedRun: in.ConfirmedRun}

	result.blockCandidate(in)
	result.blockDeployed(in)
	result.blockOrdering(in)
	result.blockArtifact(in)
	result.blockChecks(in)
	result.blockBuild(in)

	result.Eligible = len(result.Blockers) == 0
	return result
}

func (e *Eligibility) block(code, reason, remedy string) {
	e.Blockers = append(e.Blockers, Blocker{Code: code, Reason: reason, Remedy: remedy})
}

func (e *Eligibility) blockCandidate(in EligibilityInput) {
	switch {
	case !validSHA(in.Candidate.Revision.SHA):
		e.block("no_candidate_revision",
			"SafeLane does not have an exact revision to release.",
			"Name the commit to release, or let SafeLane read the default branch head.")
	case !in.Candidate.Revision.OnDefaultBranch:
		e.block("candidate_not_on_default_branch",
			fmt.Sprintf("%s is not in the default branch history of %s.", short(in.Candidate.Revision.SHA), in.Repository),
			"Merge it to the default branch first.")
	}
}

func (e *Eligibility) blockDeployed(in EligibilityInput) {
	if !validSHA(in.Deployed.SHA) {
		e.block("no_deployed_revision",
			"SafeLane cannot tell which revision is currently running, so it has nothing to compare the candidate against.",
			"Confirm the deployed commit once; SafeLane keeps exact baselines from then on.")
	}
}

// blockOrdering refuses a candidate that is not strictly newer than what is
// running.
//
// Behind means going backwards, identical means there is nothing to release,
// and diverged means the two are not on the same history at all - which is
// usually a repository or environment mixed up with another one, and is the
// case where continuing would be most quietly wrong.
func (e *Eligibility) blockOrdering(in EligibilityInput) {
	if !validSHA(in.Deployed.SHA) || !validSHA(in.Candidate.Revision.SHA) {
		return
	}
	switch in.Comparison.Status {
	case "ahead":
		return
	case "identical":
		e.block("candidate_already_deployed",
			fmt.Sprintf("%s is already what is running.", short(in.Candidate.Revision.SHA)),
			"There is nothing to release.")
	case "behind":
		e.block("candidate_older_than_deployed",
			fmt.Sprintf("%s is older than the running %s.", short(in.Candidate.Revision.SHA), short(in.Deployed.SHA)),
			"Release a commit that comes after what is running. SafeLane will not roll a release backwards.")
	case "diverged":
		e.block("unrelated_histories",
			fmt.Sprintf("%s and the running %s are not on the same history.", short(in.Candidate.Revision.SHA), short(in.Deployed.SHA)),
			"Check that this is the right repository and environment.")
	default:
		e.block("unknown_comparison",
			fmt.Sprintf("SafeLane could not compare %s with the running %s.", short(in.Candidate.Revision.SHA), short(in.Deployed.SHA)),
			"Try again when GitHub is reachable.")
	}
}

// blockArtifact requires the selected container to be bound to this candidate,
// in this repository.
func (e *Eligibility) blockArtifact(in EligibilityInput) {
	if in.Artifact.Zero() {
		e.block("no_artifact",
			"No container has been resolved for this revision.",
			"Wait for the build to publish, or name the exact image reference.")
		return
	}
	if in.ArtifactSource.Revision == "" {
		e.block("untraceable_artifact",
			fmt.Sprintf("%s carries no proof of which commit it was built from.", in.Artifact.Reference()),
			"Publish the image with OCI source and revision labels, or confirm the binding once.")
		return
	}
	if !strings.EqualFold(in.ArtifactSource.Revision, in.Candidate.Revision.SHA) {
		e.block("artifact_is_a_different_revision",
			fmt.Sprintf("%s was built from %s, not from %s.",
				in.Artifact.Reference(), short(in.ArtifactSource.Revision), short(in.Candidate.Revision.SHA)),
			"Release the container built from the candidate, or name that revision instead.")
	}
	if in.Repository != "" && in.ArtifactSource.Source != "" && !sameRepository(in.ArtifactSource.Source, in.Repository) {
		e.block("artifact_is_a_different_repository",
			fmt.Sprintf("%s was built from %s, not from the registered %s.",
				in.Artifact.Reference(), in.ArtifactSource.Source, in.Repository),
			"Release a container built from the registered repository.")
	}
}

// blockChecks requires every check branch protection asks for to have passed
// against this exact revision.
//
// A pending check is not a passing one. Waiting is the right answer, and it is
// reported as waiting rather than as a failure, because those lead a person to
// do different things.
func (e *Eligibility) blockChecks(in EligibilityInput) {
	if !in.Protection.Protected {
		e.Notices = append(e.Notices,
			fmt.Sprintf("%s's default branch has no branch protection, so SafeLane has no required checks to verify.", in.Repository))
	} else if len(in.Protection.RequiredChecks) == 0 {
		e.Notices = append(e.Notices,
			fmt.Sprintf("%s's default branch is protected but requires no status checks.", in.Repository))
	}

	required := append([]string(nil), in.Protection.RequiredChecks...)
	sort.Strings(required)
	for _, name := range required {
		run, found := in.Checks.Run(name)
		switch {
		case !found:
			e.block("required_check_missing",
				fmt.Sprintf("The required check %q has not reported against %s.", name, short(in.Candidate.Revision.SHA)),
				"Wait for it to run, or release a revision it has already reported on.")
		case run.Status != "completed":
			e.block("required_check_pending",
				fmt.Sprintf("The required check %q is still %s against %s.", name, run.Status, short(in.Candidate.Revision.SHA)),
				"Wait for it to finish.")
		case run.Conclusion != "success":
			e.block("required_check_failed",
				fmt.Sprintf("The required check %q %s against %s.", name, run.Conclusion, short(in.Candidate.Revision.SHA)),
				"Fix it and release the revision that passes.")
		}
	}
}

// blockBuild requires a successful image-producing run for this exact
// revision, and says how it was identified.
//
// Three ways, in order of how little a person has to do:
//
//  1. The container's provenance names the run. Nothing to ask.
//  2. Branch protection already required the checks, and they passed. The
//     required checks are the answer.
//  3. Neither. SafeLane lists the successful runs for the exact candidate and
//     asks which one produced this container. That answer is stored with the
//     release, not as configuration, because the next release is a different
//     build and deserves the same question.
func (e *Eligibility) blockBuild(in EligibilityInput) {
	if in.ArtifactSource.Method == oci.BindingCIProvenance {
		return
	}
	if in.Protection.Protected && len(in.Protection.RequiredChecks) > 0 {
		return
	}

	successful := in.Checks.SuccessfulWorkflows()
	if len(successful) == 0 {
		e.block("no_successful_build",
			fmt.Sprintf("No workflow run succeeded for %s, so nothing is known to have produced this container.",
				short(in.Candidate.Revision.SHA)),
			"Wait for the build to finish, or fix it.")
		return
	}
	if in.ConfirmedRun == 0 {
		e.block("build_not_confirmed",
			fmt.Sprintf("%s has %s that could have produced this container, and SafeLane cannot tell which one did.",
				short(in.Candidate.Revision.SHA), runCount(len(successful))),
			"Confirm which run produced it: "+describeRuns(successful)+".")
		return
	}
	for _, run := range successful {
		if run.ID == in.ConfirmedRun {
			return
		}
	}
	e.block("confirmed_build_not_found",
		fmt.Sprintf("Run %d is not a successful run for %s.", in.ConfirmedRun, short(in.Candidate.Revision.SHA)),
		"Confirm one of: "+describeRuns(successful)+".")
}

// Rejection turns an ineligible result into the typed error the rest of
// SafeLane carries. The category is evidence, never risk.
func (e Eligibility) Rejection() error {
	if e.Eligible {
		return nil
	}
	var errs release.Errors
	for _, blocker := range e.Blockers {
		errs = append(errs, release.FailedEvidenceError(blocker.Code, "eligibility", blocker.Reason, blocker.Remedy))
	}
	return errs.OrNil()
}

func runCount(n int) string {
	if n == 1 {
		return "one successful workflow run"
	}
	return fmt.Sprintf("%d successful workflow runs", n)
}

func describeRuns(runs []WorkflowRun) string {
	parts := make([]string, 0, len(runs))
	for _, run := range runs {
		parts = append(parts, fmt.Sprintf("%s (run %d)", run.Name, run.ID))
	}
	return strings.Join(parts, ", ")
}

// sameRepository compares a source URL with a registered `owner/name`, without
// caring about the shapes the same repository gets written in.
func sameRepository(source, repository string) bool {
	trimmed := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(source), "/"), ".git")
	if index := strings.Index(trimmed, "github.com/"); index >= 0 {
		trimmed = trimmed[index+len("github.com/"):]
	}
	return strings.EqualFold(trimmed, strings.TrimSpace(repository))
}

func short(sha string) string {
	if len(sha) <= 7 {
		return sha
	}
	return sha[:7]
}
