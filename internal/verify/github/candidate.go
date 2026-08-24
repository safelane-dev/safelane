package github

import (
	"context"
	"fmt"
	"strings"

	"github.com/AndrewMaged814/safelane/internal/release"
)

// Candidate is the exact source revision a release is about, and how it was
// chosen.
//
// There is no such thing as an approximate candidate. Either the user named a
// revision, or SafeLane read the default branch's head at the moment the
// release started - and either way the answer is one full SHA that everything
// downstream describes.
type Candidate struct {
	Revision Revision `json:"revision"`
	// Requested is true when the user named this revision, false when it was
	// read from the default branch.
	Requested bool `json:"requested"`
	// DefaultBranch is the branch the head was read from, for saying where
	// this came from.
	DefaultBranch string `json:"default_branch,omitempty"`
}

// SelectCandidate resolves what is being released.
//
// With no revision given, the candidate is the default branch's head *as
// observed now*. That observation is the candidate for the rest of the
// release: re-reading it later would silently release something the user never
// saw.
//
// A named revision has to be real and on the default branch's history. A
// commit that exists on somebody's fork is a commit that exists, and it is not
// something the registered repository has agreed to ship.
func SelectCandidate(ctx context.Context, source Source, repository, requested string) (Candidate, error) {
	if strings.TrimSpace(requested) == "" {
		head, err := source.DefaultHead(ctx, repository)
		if err != nil {
			return Candidate{}, unreachable(repository, "default branch head", err)
		}
		if !validSHA(head.SHA) {
			return Candidate{}, release.UnknownEvidenceError("no_default_head", "revision",
				fmt.Sprintf("could not read the head of %s's default branch", repository),
				"Try again when GitHub is reachable.")
		}
		return Candidate{Revision: head}, nil
	}

	if !validSHA(requested) {
		return Candidate{}, release.Invalid("invalid_revision", "revision",
			fmt.Sprintf("%q is not a full commit SHA", requested),
			"Give the full forty-character commit SHA, or leave it out to release the default branch head.")
	}

	revision, err := source.Revision(ctx, repository, requested)
	if err != nil {
		return Candidate{}, release.MissingEvidenceError("revision_not_found", "revision",
			fmt.Sprintf("%s is not a commit in %s", requested, repository),
			"Name a commit that exists in the registered repository.").WithCause(err)
	}
	if !revision.OnDefaultBranch {
		return Candidate{}, release.FailedEvidenceError("revision_not_on_default_branch", "revision",
			fmt.Sprintf("%s exists in %s but is not in its default branch history", requested, repository),
			"Merge it to the default branch first, or release a commit that is already there.")
	}
	return Candidate{Revision: revision, Requested: true}, nil
}

func validSHA(sha string) bool {
	if len(sha) != 40 {
		return false
	}
	for _, c := range sha {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

func unreachable(repository, what string, err error) error {
	return release.UnknownEvidenceError("github_unreachable", "repository",
		fmt.Sprintf("could not read %s from %s: %v", what, repository, err),
		"Try again when GitHub is reachable.")
}
