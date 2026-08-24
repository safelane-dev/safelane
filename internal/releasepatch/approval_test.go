package releasepatch_test

import (
	"testing"

	"github.com/AndrewMaged814/safelane/internal/releasepatch"
)

func granted(t *testing.T) (releasepatch.Approval, releasepatch.Patch) {
	t.Helper()
	patch := build(t)
	approval, err := releasepatch.Grant("payments-api", "production",
		"sha256:snapshot", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		patch, []string{"success-rate"}, true, at(0))
	if err != nil {
		t.Fatal(err)
	}
	return approval, patch
}

func facts(patch releasepatch.Patch) releasepatch.Facts {
	return releasepatch.Facts{
		Revision:        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Digest:          "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RunningImage:    previousImage,
		ConfigHash:      "sha256:config",
		RolloutUID:      patch.RolloutUID,
		ResourceVersion: patch.ResourceVersion,
		PatchDigest:     patch.Digest(),
	}
}

// An approval binds all six things, because approval means "yes to this" and
// every one of them is part of what "this" is.
func TestAnApprovalBindsWhatItApproves(t *testing.T) {
	approval, patch := granted(t)

	if approval.Application != "payments-api" || approval.Environment != "production" {
		t.Errorf("approval = %+v", approval)
	}
	if approval.Snapshot != "sha256:snapshot" {
		t.Errorf("snapshot = %q", approval.Snapshot)
	}
	if approval.Digest != "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("digest = %q", approval.Digest)
	}
	if approval.PatchDigest != patch.Digest() {
		t.Errorf("patch digest = %q", approval.PatchDigest)
	}
	if len(approval.Analysis) != 1 || approval.Analysis[0] != "success-rate" {
		t.Errorf("analysis = %v", approval.Analysis)
	}
}

// There is nothing to run. Letting a person approve a waiting recommendation
// would turn "I recommend waiting" into a speed bump.
func TestAWaitingRecommendationCannotBeApproved(t *testing.T) {
	_, err := releasepatch.Grant("payments-api", "production", "sha256:snapshot",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", build(t), nil, false, at(0))
	assertRejection(t, err, "cannot_approve_a_waiting_recommendation")
}

func TestAnUnchangedRecheckPasses(t *testing.T) {
	approval, patch := granted(t)
	if _, err := approval.Recheck(facts(patch), facts(patch), at(1)); err != nil {
		t.Fatalf("Recheck: %v", err)
	}
}

// Any material change cancels. There is no "close enough": the whole value of
// an approval is that it refers to something specific.
func TestAnyMaterialChangeCancelsTheApproval(t *testing.T) {
	_, patch := granted(t)
	for name, mutate := range map[string]func(*releasepatch.Facts){
		"a different candidate revision": func(f *releasepatch.Facts) { f.Revision = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" },
		"a different candidate image":    func(f *releasepatch.Facts) { f.Digest = "sha256:different" },
		"something else deployed":        func(f *releasepatch.Facts) { f.RunningImage = "ghcr.io/acme/payments-api@sha256:eee" },
		"changed configuration":          func(f *releasepatch.Facts) { f.ConfigHash = "sha256:other" },
		"a replaced Rollout":             func(f *releasepatch.Facts) { f.RolloutUID = "another-uid" },
		"a changed Rollout":              func(f *releasepatch.Facts) { f.ResourceVersion = "84999" },
		"a different patch":              func(f *releasepatch.Facts) { f.PatchDigest = "sha256:other" },
		"changed health analysis":        func(f *releasepatch.Facts) { f.AnalysisDigest = "sha256:other-analysis" },
	} {
		t.Run(name, func(t *testing.T) {
			approval, _ := granted(t)
			current := facts(patch)
			mutate(&current)

			cancelled, err := approval.Recheck(facts(patch), current, at(1))
			assertRejection(t, err, "approval_cancelled")
			if cancelled.CancelledAt.IsZero() || cancelled.CancelledBecause == "" {
				t.Errorf("the cancellation was not recorded: %+v", cancelled)
			}
			// A cancelled approval cannot then be used.
			if _, useErr := cancelled.Use(at(2)); useErr == nil {
				t.Error("a cancelled approval was spent anyway")
			}
		})
	}
}

// An approval that could be applied twice is not an approval of one release.
func TestAnApprovalCannotBeUsedTwice(t *testing.T) {
	approval, patch := granted(t)

	used, err := approval.Use(at(1))
	if err != nil {
		t.Fatalf("Use: %v", err)
	}
	if used.UsedAt.IsZero() {
		t.Error("using an approval did not record when")
	}

	if _, err := used.Use(at(2)); err == nil {
		t.Fatal("the approval was used twice")
	}
	if _, err := used.Recheck(facts(patch), facts(patch), at(2)); err == nil {
		t.Fatal("a spent approval passed a recheck")
	}
}

// A bare "yes" is consent only when it answers the approval question. The same
// word said while agreeing with the assessment is agreement about a fact, and
// treating it as consent to deploy would be putting words in somebody's mouth
// at the worst possible moment.
func TestABareYesCountsOnlyAsTheAnswerToTheApprovalQuestion(t *testing.T) {
	for _, phrase := range []string{"yes", "y", "yeah", "ok", "sure"} {
		if releasepatch.IsApproval(phrase, false) {
			t.Errorf("%q during assessment was treated as approval", phrase)
		}
		if !releasepatch.IsApproval(phrase, true) {
			t.Errorf("%q answering the approval question was not treated as approval", phrase)
		}
	}
}

// The unambiguous phrases mean what they say, wherever they are said.
func TestExplicitApprovalPhrasesAreAlwaysApproval(t *testing.T) {
	for _, phrase := range []string{"approve", "go ahead", "release it", "yes, proceed", "Approve.", "SHIP IT"} {
		if !releasepatch.IsApproval(phrase, false) {
			t.Errorf("%q was not treated as approval", phrase)
		}
	}
}

func TestOrdinaryConversationIsNotApproval(t *testing.T) {
	for _, phrase := range []string{
		"yes, that's the migration I meant",
		"that sounds right",
		"I think so",
		"no",
		"",
	} {
		if releasepatch.IsApproval(phrase, true) {
			t.Errorf("%q was treated as approval", phrase)
		}
	}
}
