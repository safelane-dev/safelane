package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/AndrewMaged814/safelane/internal/config"
	"github.com/AndrewMaged814/safelane/internal/delta"
	"github.com/AndrewMaged814/safelane/internal/discovery"
	"github.com/AndrewMaged814/safelane/internal/release"
	"github.com/AndrewMaged814/safelane/internal/verify/github"
	"github.com/AndrewMaged814/safelane/internal/verify/oci"
)

// InspectOptions are everything `safelane inspect <env> [<revision>]` needs.
//
// The four readers are ports rather than concrete clients, so the whole
// command runs against fixtures. That matters more here than anywhere else:
// this is the one place that decides what an assessment is allowed to see, and
// a path that can only be exercised against a real cluster and a real registry
// is a path nobody exercises.
type InspectOptions struct {
	Root        string
	Home        string
	Environment string
	// Revision is the optional second positional. Empty means the default
	// branch head as observed now.
	Revision  string
	App       string
	ForceJSON bool

	Cluster  discovery.Service
	Source   github.Source
	Registry oci.Resolver
	// History supplies this Application and Environment's compact history.
	// Nil means none is available yet.
	History func(application, environment string) ([]delta.HistoryCard, error)
	// Now is the capture clock. Nil means the real one.
	Now func() time.Time
}

// Inspect freezes the evidence boundary for one release and reports the four
// views.
//
// It stops before assessment when the release is not eligible, and says so in
// eligibility language. "Your build failed" is not a risk level.
func Inspect(ctx context.Context, opts InspectOptions, stdout, stderr io.Writer) int {
	frozen, eligibility, err := FreezeDelta(ctx, opts)
	if err != nil {
		return writeResultError(stderr, "inspect", err)
	}
	if !eligibility.Eligible {
		renderIneligible(stderr, eligibility)
		return ExitFail
	}

	if RenderingFor(stdout, opts.ForceJSON) == RenderJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(map[string]any{
			"snapshot_id": frozen.SnapshotID(),
			"delta":       frozen,
			"views":       frozen.Views(),
			"handles":     frozen.Handles(),
			"eligibility": eligibility,
		}); err != nil {
			return writeResultError(stderr, "inspect", err)
		}
		return ExitOK
	}

	fmt.Fprintf(stdout, "snapshot %s\n\n", frozen.SnapshotID())
	for _, name := range delta.ViewNames {
		view, _ := frozen.View(name)
		fmt.Fprintf(stdout, "== %s ==\n%s\n", name, strings.TrimRight(view, "\n"))
	}
	for _, notice := range eligibility.Notices {
		fmt.Fprintf(stdout, "\nnote  %s\n", notice)
	}
	return ExitOK
}

// FreezeDelta gathers the evidence and closes the boundary.
//
// The order is the order the facts depend on each other: what is running,
// then what would be, then whether that is allowed at all, and only then the
// frozen record of it.
func FreezeDelta(ctx context.Context, opts InspectOptions) (delta.ReleaseDelta, github.Eligibility, error) {
	application, err := applicationFrom(opts.Root, opts.Home, opts.App, opts.Cluster.Origin)
	if err != nil {
		return delta.ReleaseDelta{}, github.Eligibility{}, err
	}
	cfg, err := config.Load(config.ForApp(opts.Home, application).File)
	if err != nil {
		return delta.ReleaseDelta{}, github.Eligibility{}, err
	}
	environment, ok := cfg.Environment(opts.Environment)
	if !ok {
		return delta.ReleaseDelta{}, github.Eligibility{}, unknownEnvironment(application, opts.Environment, cfg)
	}

	target, err := opts.Cluster.Inspect(ctx, opts.Root, environment.Kubernetes.Namespace, environment.Kubernetes.Rollout)
	if err != nil {
		return delta.ReleaseDelta{}, github.Eligibility{}, err
	}
	container, found := target.SelectedContainer(cfg.Artifact.Container)
	if !found {
		return delta.ReleaseDelta{}, github.Eligibility{}, release.Invalid("container_not_found", "artifact.container",
			fmt.Sprintf("Rollout %q no longer has a container called %q", target.Rollout, cfg.Artifact.Container),
			"Register this application again to pick up the change.")
	}

	baseline, err := bindRunning(ctx, opts.Registry, cfg.Artifact.Image, container.Image)
	if err != nil {
		return delta.ReleaseDelta{}, github.Eligibility{}, err
	}

	candidate, err := github.SelectCandidate(ctx, opts.Source, cfg.Application.Repository, opts.Revision)
	if err != nil {
		return delta.ReleaseDelta{}, github.Eligibility{}, err
	}
	candidateArtifact, candidateSource := findCandidateArtifact(ctx, opts.Registry,
		cfg.Artifact.Image, cfg.Application.Repository, candidate.Revision.SHA)

	protection, _ := opts.Source.Repository(ctx, ownerOf(cfg.Application.Repository), nameOf(cfg.Application.Repository))
	checks, _ := opts.Source.Checks(ctx, cfg.Application.Repository, candidate.Revision.SHA)
	comparison, _ := opts.Source.Compare(ctx, cfg.Application.Repository, baseline.revision, candidate.Revision.SHA)

	eligibility := github.EvaluateEligibility(github.EligibilityInput{
		Repository:     cfg.Application.Repository,
		Candidate:      candidate,
		Deployed:       github.Revision{SHA: baseline.revision, OnDefaultBranch: true},
		Artifact:       candidateArtifact,
		ArtifactSource: candidateSource,
		Protection:     protection,
		Checks:         checks,
		Comparison:     comparison,
	})

	history := []delta.HistoryCard(nil)
	if opts.History != nil {
		history, _ = opts.History(application, environment.Name)
	}

	lane, weights := defaultLane(cfg)
	frozen := delta.Freeze(delta.Input{
		Application: application,
		Environment: environment.Name,
		Baseline: delta.ArtifactBinding{
			Image: cfg.Artifact.Image, Digest: baseline.artifact.Digest,
			Revision: baseline.revision, Source: baseline.source.Source,
			Method: string(baseline.source.Method),
		},
		Candidate: delta.ArtifactBinding{
			Image: cfg.Artifact.Image, Digest: candidateArtifact.Digest,
			Revision: candidate.Revision.SHA, Source: candidateSource.Source,
			Method:  string(candidateSource.Method),
			Subject: delta.Untrusted(candidate.Revision.Subject),
		},
		Changes:    changeSetFrom(comparison),
		Deployment: deploymentFrom(cfg, environment, target, container, candidateArtifact, lane, weights),
		Health:     healthFrom(target),
		History:    history,
		CapturedAt: opts.now(),
	})
	return frozen, eligibility, nil
}

func (o InspectOptions) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now().UTC()
}

// runningBinding is what is deployed right now, and how SafeLane knows.
type runningBinding struct {
	artifact oci.Artifact
	source   oci.SourceMetadata
	revision string
}

// bindRunning resolves the image the Rollout is actually running and asks what
// it was built from.
//
// A failure to prove that is not a failure to inspect: "SafeLane cannot tell
// what is running" is one of the eligibility blockers, and it belongs there
// with the others rather than as an error that hides the rest of the evidence.
func bindRunning(ctx context.Context, resolver oci.Resolver, repository, live string) (runningBinding, error) {
	reference := live
	if index := strings.LastIndex(live, "@"); index >= 0 {
		reference = live[index+1:]
	} else if index := strings.LastIndex(live, ":"); index > strings.LastIndex(live, "/") {
		reference = live[index+1:]
	}

	artifact, err := resolver.Resolve(ctx, repository, reference)
	if err != nil {
		return runningBinding{}, nil
	}
	source, err := resolver.ReadSource(ctx, artifact)
	if err != nil {
		return runningBinding{artifact: artifact}, nil
	}
	return runningBinding{artifact: artifact, source: source, revision: source.Revision}, nil
}

// findCandidateArtifact looks for the container built from the candidate.
//
// Not finding one is an eligibility answer, not an error: "nothing is
// published for that commit yet" is a thing a person needs told alongside
// everything else, not instead of it.
func findCandidateArtifact(ctx context.Context, resolver oci.Resolver, repository, source, revision string) (oci.Artifact, oci.SourceMetadata) {
	artifact, err := resolver.FindRevision(ctx, repository, "https://github.com/"+source, revision)
	if err != nil {
		return oci.Artifact{}, oci.SourceMetadata{}
	}
	metadata, err := resolver.ReadSource(ctx, artifact)
	if err != nil {
		return artifact, oci.SourceMetadata{}
	}
	return artifact, metadata
}

func changeSetFrom(comparison github.Comparison) delta.ChangeSet {
	set := delta.ChangeSet{
		Base: comparison.Base, Head: comparison.Head,
		Status: comparison.Status, AheadBy: comparison.AheadBy,
	}
	for _, commit := range comparison.Commits {
		set.Commits = append(set.Commits, delta.Commit{
			SHA: commit.SHA, Subject: delta.Untrusted(commit.Subject),
			Author: delta.Untrusted(commit.Author), CommittedAt: commit.CommittedAt,
		})
	}
	for _, file := range comparison.Files {
		set.Files = append(set.Files, delta.File{
			Path: delta.Untrusted(file.Path), Status: file.Status,
			Additions: file.Additions, Deletions: file.Deletions,
		})
	}
	for _, pr := range comparison.PullRequests {
		set.PullRequests = append(set.PullRequests, delta.PullRequest{
			Number: pr.Number, Title: delta.Untrusted(pr.Title),
			Branch: delta.Untrusted(pr.Summary), Merge: pr.Merge,
		})
	}
	return set
}

func deploymentFrom(cfg config.Config, environment config.Environment, target discovery.Target,
	container discovery.Container, artifact oci.Artifact, lane string, weights []int) delta.DeploymentEvidence {

	image := artifact.Reference()
	if artifact.Zero() {
		image = ""
	}
	return delta.DeploymentEvidence{
		Environment: environment.Name,
		Impact:      string(environment.Impact),
		Context:     environment.Kubernetes.Context,
		Namespace:   environment.Kubernetes.Namespace,
		Rollout:     environment.Kubernetes.Rollout,
		Container:   container.Name,
		Mechanism:   exposureMechanism(target),
		Replicas:    delta.ReplicasIn(target.RolloutJSON),
		// Names only. The live Rollout carries values; this reads references
		// out of it and the object itself never crosses the boundary.
		SecretReferences: delta.SecretReferencesIn(target.RolloutJSON),
		Patch: delta.Patch{
			ContainerIndex: containerIndex(target, container.Name),
			Image:          image,
			Lane:           lane,
			Weights:        weights,
		},
	}
}

// exposureMechanism describes how honestly the weights map to traffic. A
// replica-approximated weight is not a traffic percentage, and calling it one
// would overstate what the canary actually proved.
func exposureMechanism(target discovery.Target) string {
	if target.CanaryService == "" {
		return "no canary Service; exposure cannot be described"
	}
	return fmt.Sprintf("canary Service %s; exposure is approximated by replica count, not measured request traffic",
		target.CanaryService)
}

func containerIndex(target discovery.Target, name string) int {
	for i, container := range target.Containers {
		if container.Name == name {
			return i
		}
	}
	return 0
}

func healthFrom(target discovery.Target) []delta.HealthObjective {
	objectives := make([]delta.HealthObjective, 0, len(target.Analysis))
	for _, analysis := range target.Analysis {
		scope := ""
		if target.CanaryService != "" {
			scope = "the canary Service " + target.CanaryService
		}
		objectives = append(objectives, delta.HealthObjective{
			Name: delta.Untrusted(analysis.Name), Provider: analysis.Provider,
			Condition: delta.Untrusted(analysis.Condition),
			Interval:  analysis.Interval, InitialDelay: analysis.InitialDelay,
			Scope: scope, Resolved: analysis.Resolved,
		})
	}
	return objectives
}

// defaultLane is the lane the proposal starts from. The recommendation may
// choose a different one; until it does, the cautious configured lane is what
// SafeLane is proposing, and the deployment view says which.
func defaultLane(cfg config.Config) (string, []int) {
	name, lane, err := cfg.Policy.LaneFor("")
	if err != nil {
		return "", nil
	}
	return name, lane.Weights
}

func renderIneligible(w io.Writer, eligibility github.Eligibility) {
	fmt.Fprintln(w, "This release cannot go ahead yet.")
	for _, blocker := range eligibility.Blockers {
		fmt.Fprintf(w, "\n  %s\n  %s\n", blocker.Reason, blocker.Remedy)
	}
	for _, notice := range eligibility.Notices {
		fmt.Fprintf(w, "\nnote  %s\n", notice)
	}
}

func ownerOf(repository string) string {
	owner, _, _ := strings.Cut(repository, "/")
	return owner
}

func nameOf(repository string) string {
	_, name, _ := strings.Cut(repository, "/")
	return name
}

// ConfigHash content-addresses an Application's configuration file.
//
// It is part of the pre-apply recheck: an approval was given against a
// particular set of lanes and a particular target, and a configuration that
// changed in between makes the approval about something else.
func ConfigHash(home, application string) string {
	raw, err := os.ReadFile(config.ForApp(home, application).File)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func unknownEnvironment(application, environment string, cfg config.Config) error {
	return release.Invalid("unknown_environment", "environment",
		fmt.Sprintf("%s is not a registered environment for %s", environment, application),
		"Register it, or name one of: "+strings.Join(cfg.EnvironmentNames(), ", ")+".")
}
