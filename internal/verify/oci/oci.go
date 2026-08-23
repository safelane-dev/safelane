package oci

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/AndrewMaged814/safelane/internal/release"
)

// Artifact is one immutable container image. There is no tag on it: a tag is a
// pointer, and this type is the thing pointed at.
type Artifact struct {
	// Repository is the OCI repository, e.g. `ghcr.io/acme/payments-api`.
	Repository string `json:"repository"`
	// Digest is the manifest digest, `sha256:` followed by 64 hex characters.
	Digest string `json:"digest"`
}

// Reference is the immutable reference an executor can apply.
func (a Artifact) Reference() string { return a.Repository + "@" + a.Digest }

// Zero reports whether nothing was resolved.
func (a Artifact) Zero() bool { return a.Repository == "" || a.Digest == "" }

// BindingMethod names where an Artifact-to-revision binding came from. It is
// recorded in evidence in every case, because "how do you know" is a different
// question from "what do you think", and only one of them can be audited.
type BindingMethod string

const (
	// BindingOCILabels is the image's own source and revision labels.
	BindingOCILabels BindingMethod = "oci_labels"
	// BindingCIProvenance is a build system's attestation.
	BindingCIProvenance BindingMethod = "ci_provenance"
	// BindingHistory is SafeLane's own record of having released it.
	BindingHistory BindingMethod = "safelane_history"
	// BindingConfirmedBaseline is a person naming the deployed commit once,
	// after SafeLane confirmed that commit exists in the registered repository.
	BindingConfirmedBaseline BindingMethod = "confirmed_baseline"
)

// SourceMetadata is a proved binding from an Artifact to the source it was
// built from.
type SourceMetadata struct {
	// Source is the source repository the image claims, e.g.
	// `https://github.com/acme/payments-api`.
	Source string `json:"source"`
	// Revision is the full commit SHA. Never abbreviated: a short SHA is a
	// prefix, and a prefix is a guess with good odds.
	Revision string `json:"revision"`
	// Method is where this binding came from.
	Method BindingMethod `json:"method"`
}

// Registry is the read surface this package needs from an OCI registry. It is
// an interface so the whole resolver runs against an in-process registry, or a
// fake, with no network.
type Registry interface {
	// Resolve turns any reference - a tag or a digest - into a manifest digest.
	Resolve(ctx context.Context, repository, reference string) (string, error)
	// Platforms returns the config labels of every runnable platform under a
	// digest. A single-platform image returns one entry; an index returns one
	// per runnable manifest, with attestation manifests left out.
	Platforms(ctx context.Context, repository, digest string) ([]PlatformLabels, error)
	// Tags lists the repository's tags.
	Tags(ctx context.Context, repository string) ([]string, error)
}

// PlatformLabels are one runnable platform's identity and config labels.
type PlatformLabels struct {
	// Platform is `os/architecture`, for naming the odd one out.
	Platform string
	Labels   map[string]string
}

// BindingSource is a place other than the image's own labels that can prove
// the same binding: a CI attestation, or SafeLane's own history.
//
// It is a port rather than an implementation because both of those live
// elsewhere - provenance with the build system, history with the store - and
// because the resolver's rule is the same either way: ask, and believe a full
// answer or none of it.
type BindingSource interface {
	// Bind returns the binding this source can prove for an Artifact. The
	// boolean is false when it simply does not know, which is not an error.
	Bind(ctx context.Context, artifact Artifact) (SourceMetadata, bool, error)
}

// RevisionChecker confirms a commit exists in the registered repository. It is
// what stops the one human escape hatch from being a way to type any forty
// hex characters and have SafeLane believe them.
type RevisionChecker interface {
	RevisionExists(ctx context.Context, repository, revision string) (bool, error)
}

// Resolver is the package's whole behaviour.
type Resolver struct {
	// Registry is the registry read surface. Required.
	Registry Registry
	// Extra are consulted in order when the image's own labels do not prove a
	// binding. Typically CI provenance first, then SafeLane's history.
	Extra []BindingSource
	// MaxTags bounds tag enumeration in [Resolver.FindRevision]. Zero means
	// the default below.
	MaxTags int
}

// defaultMaxTags bounds how far FindRevision will look. A repository with
// thousands of tags is a repository where the honest answer is "give me the
// exact reference" rather than "wait while I read all of them".
const defaultMaxTags = 200

// The two labels that carry provenance. Both are required: a source with no
// revision names a project, and a revision with no source names a commit in
// some repository or other.
const (
	labelSource   = "org.opencontainers.image.source"
	labelRevision = "org.opencontainers.image.revision"
)

// Resolve turns a tag or digest into the immutable Artifact it names.
//
// Everything downstream works from the result, so this happens before anything
// else: assessment, patching and proof all describe one digest, and a tag that
// moved between reading and deploying would make all three describe something
// that is no longer true.
func (r Resolver) Resolve(ctx context.Context, repository, reference string) (Artifact, error) {
	if strings.TrimSpace(repository) == "" || strings.TrimSpace(reference) == "" {
		return Artifact{}, release.Invalid("missing_image_reference", "artifact",
			"no image was named",
			"Name the image repository and the tag or digest to release.")
	}
	digest, err := r.Registry.Resolve(ctx, repository, reference)
	if err != nil {
		return Artifact{}, unreachable(repository, reference, err)
	}
	if !validDigest(digest) {
		return Artifact{}, release.UnknownEvidenceError("unresolved_image", "artifact",
			fmt.Sprintf("%s:%s did not resolve to a digest", repository, reference),
			"Check the tag exists, and that the registry is reachable.")
	}
	return Artifact{Repository: repository, Digest: digest}, nil
}

// ReadSource proves which revision an Artifact was built from.
//
// It reads every runnable platform's labels and requires them to agree. A
// multi-platform index whose arm64 image says one commit and whose amd64 image
// says another is not a build of one revision; it is two builds sharing a tag,
// and which one a cluster runs depends on which node it lands on.
//
// When the labels prove nothing, each configured [BindingSource] is asked in
// turn. When none of them knows either, this is a failure and not a guess.
func (r Resolver) ReadSource(ctx context.Context, artifact Artifact) (SourceMetadata, error) {
	if artifact.Zero() {
		return SourceMetadata{}, release.Invalid("missing_image_reference", "artifact",
			"no artifact was given", "Resolve the image to a digest first.")
	}

	platforms, err := r.Registry.Platforms(ctx, artifact.Repository, artifact.Digest)
	if err != nil {
		return SourceMetadata{}, unreachable(artifact.Repository, artifact.Digest, err)
	}

	labelled, conflict := readLabels(platforms)
	if conflict != nil {
		return SourceMetadata{}, conflict
	}
	if labelled.Revision != "" {
		return labelled, nil
	}

	for _, source := range r.Extra {
		binding, ok, bindErr := source.Bind(ctx, artifact)
		if bindErr != nil {
			return SourceMetadata{}, bindErr
		}
		if !ok {
			continue
		}
		if err := binding.validate(); err != nil {
			return SourceMetadata{}, err
		}
		return binding, nil
	}

	return SourceMetadata{}, release.MissingEvidenceError("unbound_artifact", "artifact",
		fmt.Sprintf("%s carries no source metadata, and no build provenance or SafeLane history binds it to a commit", artifact.Reference()),
		"Publish the image with org.opencontainers.image.source and org.opencontainers.image.revision, or confirm the deployed commit once.")
}

// FindRevision finds the Artifact built from a requested revision.
//
// It enumerates tags, resolves each, and reads verified source metadata. The
// match is on the proved revision, never on the tag's spelling: a tag called
// `sha-abc123` is a string somebody chose, and a repository where that
// convention holds is a repository where the labels agree anyway.
//
// When nothing matches, it says so. It does not return the newest tag, the
// closest tag, or the one that happens to be `latest`, because deploying an
// older container than the one asked for is the accident this package exists
// to prevent.
func (r Resolver) FindRevision(ctx context.Context, repository, source, revision string) (Artifact, error) {
	if !validRevision(revision) {
		return Artifact{}, release.Invalid("invalid_revision", "revision",
			fmt.Sprintf("%q is not a full commit SHA", revision),
			"Give the full forty-character commit SHA.")
	}

	tags, err := r.Registry.Tags(ctx, repository)
	if err != nil {
		return Artifact{}, unreachable(repository, "tags", err)
	}
	sort.Strings(tags)

	limit := r.MaxTags
	if limit <= 0 {
		limit = defaultMaxTags
	}
	truncated := false
	if len(tags) > limit {
		tags, truncated = tags[:limit], true
	}

	seen := map[string]bool{}
	for _, tag := range tags {
		artifact, resolveErr := r.Resolve(ctx, repository, tag)
		if resolveErr != nil {
			continue
		}
		if seen[artifact.Digest] {
			continue
		}
		seen[artifact.Digest] = true

		metadata, sourceErr := r.ReadSource(ctx, artifact)
		if sourceErr != nil {
			continue
		}
		if !strings.EqualFold(metadata.Revision, revision) {
			continue
		}
		if source != "" && !sameSource(metadata.Source, source) {
			continue
		}
		return artifact, nil
	}

	detail := fmt.Sprintf("no image in %s is built from %s", repository, revision)
	if truncated {
		detail += fmt.Sprintf(" among its %d most recent tags", limit)
	}
	return Artifact{}, release.MissingEvidenceError("revision_not_published", "artifact",
		detail,
		"Wait for the build to publish, or name the exact image reference to release.")
}

// ConfirmBaseline is the one escape hatch, and it is used once.
//
// When the running container carries no usable provenance, a person names the
// deployed commit. SafeLane confirms that commit exists in the registered
// repository before binding it, and records that a human said so rather than
// that the registry did. From the first SafeLane release onwards, history
// supplies exact baselines and this is not needed again.
func (r Resolver) ConfirmBaseline(ctx context.Context, checker RevisionChecker, artifact Artifact, repository, revision string) (SourceMetadata, error) {
	if artifact.Zero() {
		return SourceMetadata{}, release.Invalid("missing_image_reference", "artifact",
			"no artifact was given", "Resolve the running image to a digest first.")
	}
	if !validRevision(revision) {
		return SourceMetadata{}, release.Invalid("invalid_revision", "revision",
			fmt.Sprintf("%q is not a full commit SHA", revision),
			"Give the full forty-character commit SHA of what is deployed.")
	}
	if checker == nil {
		return SourceMetadata{}, release.Internal("no_revision_checker",
			"no way to confirm the commit exists")
	}

	exists, err := checker.RevisionExists(ctx, repository, revision)
	if err != nil {
		return SourceMetadata{}, release.UnknownEvidenceError("revision_unknown", "revision",
			fmt.Sprintf("could not confirm %s exists in %s: %v", revision, repository, err),
			"Try again when GitHub is reachable.")
	}
	if !exists {
		return SourceMetadata{}, release.FailedEvidenceError("revision_not_in_repository", "revision",
			fmt.Sprintf("%s is not a commit in %s", revision, repository),
			"Name a commit that exists in the registered repository.")
	}

	return SourceMetadata{
		Source:   "https://github.com/" + repository,
		Revision: revision,
		Method:   BindingConfirmedBaseline,
	}, nil
}

// readLabels folds every runnable platform's labels into one binding, or
// reports the disagreement.
func readLabels(platforms []PlatformLabels) (SourceMetadata, error) {
	var agreed SourceMetadata
	var agreedOn string

	for _, platform := range platforms {
		source := strings.TrimSpace(platform.Labels[labelSource])
		revision := strings.TrimSpace(platform.Labels[labelRevision])

		// A partial answer is no answer. A source with no revision names a
		// project; a revision with no source names a commit in some repository
		// or other.
		if source == "" || revision == "" {
			if agreed.Revision != "" {
				return SourceMetadata{}, inconsistent(agreedOn, platform.Platform,
					"one platform carries source metadata and another does not")
			}
			continue
		}
		if !validRevision(revision) {
			return SourceMetadata{}, release.FailedEvidenceError("malformed_source_metadata", "artifact",
				fmt.Sprintf("platform %s labels its revision as %q, which is not a full commit SHA", platform.Platform, revision),
				"Publish org.opencontainers.image.revision as the full forty-character commit SHA.")
		}

		if agreed.Revision == "" {
			agreed = SourceMetadata{Source: source, Revision: revision, Method: BindingOCILabels}
			agreedOn = platform.Platform
			continue
		}
		if !strings.EqualFold(agreed.Revision, revision) || !sameSource(agreed.Source, source) {
			return SourceMetadata{}, inconsistent(agreedOn, platform.Platform,
				fmt.Sprintf("%s says %s and %s says %s", agreedOn, agreed.Revision, platform.Platform, revision))
		}
	}
	return agreed, nil
}

func inconsistent(first, second, detail string) error {
	return release.FailedEvidenceError("inconsistent_source_metadata", "artifact",
		fmt.Sprintf("the platforms in this image do not agree on what they were built from: %s", detail),
		"Rebuild every platform from one commit and publish them under one index.").
		WithCause(fmt.Errorf("platforms %s and %s disagree", first, second))
}

func (m SourceMetadata) validate() error {
	if m.Revision == "" || m.Source == "" {
		return release.MissingEvidenceError("incomplete_binding", "artifact",
			"a binding source returned a partial answer",
			"A binding must name both the source repository and the full commit SHA.")
	}
	if !validRevision(m.Revision) {
		return release.FailedEvidenceError("malformed_source_metadata", "artifact",
			fmt.Sprintf("%q is not a full commit SHA", m.Revision),
			"A binding must name the full forty-character commit SHA.")
	}
	if m.Method == "" {
		return release.Internal("unrecorded_binding_method",
			"a binding source did not say how it knows")
	}
	return nil
}

// sameSource compares source repositories without caring about the shapes the
// same repository is written in - a trailing `.git`, a trailing slash, case.
func sameSource(a, b string) bool {
	return strings.EqualFold(normaliseSource(a), normaliseSource(b))
}

func normaliseSource(s string) string {
	s = strings.TrimSuffix(strings.TrimSpace(s), "/")
	return strings.TrimSuffix(s, ".git")
}

func validDigest(digest string) bool {
	if !strings.HasPrefix(digest, "sha256:") {
		return false
	}
	return isHex(strings.TrimPrefix(digest, "sha256:"), 64)
}

func validRevision(revision string) bool { return isHex(revision, 40) }

func isHex(s string, length int) bool {
	if len(s) != length {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

func unreachable(repository, reference string, err error) error {
	return release.UnknownEvidenceError("registry_unreachable", "artifact",
		fmt.Sprintf("could not read %s (%s) from the registry: %v", repository, reference, err),
		"Check the registry is reachable, and that you are logged in if it is private.")
}
