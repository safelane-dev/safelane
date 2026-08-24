package releasepatch

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/AndrewMaged814/safelane/internal/release"
)

// Approval is one person saying yes to one exact thing, once.
//
// It binds the Application, the Environment, the candidate revision and digest,
// the patch, the configured analysis, and the recommendation it answered. All
// six, because approval means "yes to this" and every one of them is part of
// what "this" is. A yes that survived a changed digest would not be an approval
// of anything a person had seen.
type Approval struct {
	Application string    `json:"application"`
	Environment string    `json:"environment"`
	Snapshot    string    `json:"snapshot"`
	Revision    string    `json:"revision"`
	Digest      string    `json:"digest"`
	PatchDigest string    `json:"patch_digest"`
	Analysis    []string  `json:"analysis"`
	GrantedAt   time.Time `json:"granted_at"`
	// UsedAt is set the moment the patch is applied. A used approval is spent,
	// not reusable, and the record says when it was spent.
	UsedAt time.Time `json:"used_at,omitempty"`
	// CancelledAt and CancelledBecause are set when a material fact moved
	// between approval and apply.
	CancelledAt      time.Time `json:"cancelled_at,omitempty"`
	CancelledBecause string    `json:"cancelled_because,omitempty"`
}

// Grant binds an approval to everything it is an approval of.
//
// A waiting recommendation cannot be approved. There is nothing to run: the
// recommendation said so, and letting a person approve one anyway would turn
// "I recommend waiting" into a speed bump.
func Grant(application, environment, snapshot, revision string, patch Patch, analysis []string, proceeding bool, now time.Time) (Approval, error) {
	if !proceeding {
		return Approval{}, release.Invalid("cannot_approve_a_waiting_recommendation", "approval",
			"this recommendation is to wait, so there is nothing to approve",
			"Resolve what the recommendation is waiting on, and I will reassess.")
	}
	for field, value := range map[string]string{
		"application": application, "environment": environment,
		"snapshot": snapshot, "revision": revision,
	} {
		if strings.TrimSpace(value) == "" {
			return Approval{}, release.Invalid("incomplete_approval", field,
				"an approval must name the "+field+" it approves",
				"This is a SafeLane defect; nothing was changed.")
		}
	}

	return Approval{
		Application: application,
		Environment: environment,
		Snapshot:    snapshot,
		Revision:    revision,
		Digest:      digestOf(patch.CandidateImage),
		PatchDigest: patch.Digest(),
		Analysis:    append([]string(nil), analysis...),
		GrantedAt:   now,
	}, nil
}

// Digest content-addresses the patch, so an approval binds to the exact
// operations rather than to the idea of them.
func (p Patch) Digest() string {
	raw, err := p.JSON()
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Facts are the things that must not have moved between approval and apply.
//
// Not "things that usually do not move" - things whose movement makes the
// approval about something else. A different digest is a different release; a
// rewritten Rollout is a different target; a changed configuration is a
// different set of lanes.
type Facts struct {
	Revision        string
	Digest          string
	RunningImage    string
	ConfigHash      string
	RolloutUID      string
	ResourceVersion string
	PatchDigest     string
}

// Recheck compares what was true at approval with what is true now.
//
// It runs immediately before the cluster is touched, and any difference cancels
// the approval rather than proceeding carefully. There is no "close enough":
// the whole value of an approval is that it refers to something specific.
func (a Approval) Recheck(approved, current Facts, now time.Time) (Approval, error) {
	if !a.UsedAt.IsZero() {
		return a, release.Invalid("approval_already_used", "approval",
			"this approval was already used",
			"Ask for a new recommendation if you want to release again.")
	}

	for _, check := range []struct {
		name        string
		was, is     string
		explanation string
	}{
		{"candidate revision", approved.Revision, current.Revision,
			"the commit being released is not the one you approved"},
		{"candidate image", approved.Digest, current.Digest,
			"the container being released is not the one you approved"},
		{"running version", approved.RunningImage, current.RunningImage,
			"something else deployed to this environment in the meantime"},
		{"configuration", approved.ConfigHash, current.ConfigHash,
			"this application's SafeLane configuration changed"},
		{"Rollout identity", approved.RolloutUID, current.RolloutUID,
			"the Rollout was replaced"},
		{"Rollout version", approved.ResourceVersion, current.ResourceVersion,
			"the Rollout changed"},
		{"patch", approved.PatchDigest, current.PatchDigest,
			"the change SafeLane would apply is no longer the one you approved"},
	} {
		if check.was == check.is {
			continue
		}
		return a.cancel(check.explanation, now), release.Invalid("approval_cancelled", "approval",
			fmt.Sprintf("The approval no longer applies: %s.", check.explanation),
			"Nothing was changed. Ask me to look again and I will reassess from where things are now.")
	}
	return a, nil
}

// Use spends the approval. Calling it twice is refused, because an approval
// that could be applied twice is not an approval of one release.
func (a Approval) Use(now time.Time) (Approval, error) {
	if !a.UsedAt.IsZero() {
		return a, release.Invalid("approval_already_used", "approval",
			"this approval was already used",
			"Ask for a new recommendation if you want to release again.")
	}
	if !a.CancelledAt.IsZero() {
		return a, release.Invalid("approval_cancelled", "approval",
			"this approval was cancelled: "+a.CancelledBecause,
			"Ask me to look again and I will reassess.")
	}
	a.UsedAt = now
	return a, nil
}

func (a Approval) cancel(because string, now time.Time) Approval {
	a.CancelledAt = now
	a.CancelledBecause = because
	return a
}

func digestOf(image string) string {
	if index := strings.LastIndex(image, "@"); index >= 0 {
		return image[index+1:]
	}
	return ""
}

// IsApproval reads a person's words.
//
// A bare "yes" counts only as the direct answer to the approval question. The
// same word said while agreeing with the assessment - "yes, that's the
// migration I meant" - is agreement about a fact, and treating it as consent to
// deploy would be putting words in somebody's mouth at the worst possible
// moment.
func IsApproval(text string, answeringApprovalQuestion bool) bool {
	normalised := strings.ToLower(strings.TrimSpace(text))
	normalised = strings.TrimRight(normalised, ".!")

	switch normalised {
	case "approve", "approve this", "approved", "go ahead", "release it", "ship it", "yes, proceed", "proceed":
		return true
	case "yes", "y", "yep", "yeah", "ok", "okay", "sure":
		return answeringApprovalQuestion
	}
	return false
}
