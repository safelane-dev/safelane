package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/AndrewMaged814/safelane/internal/assessment"
	"github.com/AndrewMaged814/safelane/internal/config"
	"github.com/AndrewMaged814/safelane/internal/delta"
	"github.com/AndrewMaged814/safelane/internal/release"
	"github.com/AndrewMaged814/safelane/internal/releasepatch"
)

// RecommendOptions are everything `safelane recommend <env> <assessment|->`
// needs.
type RecommendOptions struct {
	Inspect InspectOptions
	// AssessmentPath is a file, or "-" for stdin.
	AssessmentPath string
	// Attempt is which submission this is for this snapshot. The first is 1;
	// the second is the one correction. Ticket 09 binds this to the release
	// record so a session cannot restart the count by resubmitting.
	Attempt int
	Stdin   io.Reader
}

// Recommend validates the session's assessment against the frozen evidence and
// prints the recommendation.
//
// SafeLane does not produce the assessment. It arrives as an external value,
// gets checked for grounding rather than for correctness, and is then rendered
// in release language.
func Recommend(ctx context.Context, opts RecommendOptions, stdout, stderr io.Writer) int {
	raw, err := readAssessment(opts)
	if err != nil {
		return writeResultError(stderr, "recommend", err)
	}
	application, err := applicationFrom(opts.Inspect.Root, opts.Inspect.Home,
		opts.Inspect.App, opts.Inspect.Cluster.Origin)
	if err != nil {
		return writeResultError(stderr, "recommend", err)
	}
	inspection, found, err := loadInspection(config.ForApp(opts.Inspect.Home, application).
		ForEnvironment(opts.Inspect.Environment).Dir)
	if err != nil {
		return writeResultError(stderr, "recommend", err)
	}
	if found {
		opts.Inspect.Revision = inspection.Revision
	}

	frozen, eligibility, err := FreezeDelta(ctx, opts.Inspect)
	if err != nil {
		return writeResultError(stderr, "recommend", err)
	}
	if !eligibility.Eligible {
		renderIneligible(stderr, eligibility)
		return ExitFail
	}

	cfg, err := configFor(opts.Inspect, frozen.Application())
	if err != nil {
		return writeResultError(stderr, "recommend", err)
	}

	environmentDir := config.ForApp(opts.Inspect.Home, frozen.Application()).
		ForEnvironment(frozen.Environment()).Dir

	// The attempt counter lives with the pending recommendation, not with the
	// caller. A session that could restart the count by resubmitting would
	// have unlimited corrections, and "eventually validated" is not the same
	// claim as "was right the first time".
	previous, hadPrevious, err := releasepatch.LoadPending(environmentDir)
	if err != nil {
		return writeResultError(stderr, "recommend", err)
	}
	attempt := 1
	if hadPrevious && previous.Snapshot == frozen.SnapshotID() {
		attempt = previous.Attempts + 1
	}
	if opts.Attempt > 0 {
		attempt = opts.Attempt
	}

	outcome := assessment.Resolve(raw, frozen, cfg.ReleaseSettings, attempt)
	if outcome.Correction != nil {
		// The failed attempt is recorded before the correction is asked for,
		// so the second one is the second one however it arrives.
		_ = releasepatch.SavePending(environmentDir, releasepatch.Pending{
			Application: frozen.Application(), Environment: frozen.Environment(),
			Snapshot: frozen.SnapshotID(), Attempts: attempt,
			Action: string(assessment.ActionWait),
		})
		fmt.Fprint(stderr, assessment.CorrectionRequest(outcome.Correction))
		return ExitDecision
	}

	recommendation := outcome.Recommendation
	if len(recommendation.Provided) > 0 {
		provided := make([]delta.ProvidedEvidence, 0, len(recommendation.Provided))
		for _, evidence := range recommendation.Provided {
			at, _ := time.Parse(time.RFC3339, evidence.At)
			provided = append(provided, delta.ProvidedEvidence{
				Kind: evidence.Kind, Value: delta.Untrusted(evidence.Value), Source: evidence.Source,
				At: at, Candidate: evidence.Candidate, Environment: evidence.Environment,
			})
		}
		frozen = frozen.WithProvided(provided)
		recommendation.SnapshotID = frozen.SnapshotID()
	}
	lane := config.Lane{}
	if recommendation.Action == assessment.ActionProceed {
		_, lane, _ = cfg.ReleaseSettings.LaneFor(config.Risk(recommendation.Risk))
		frozen = frozen.WithPatch(patchFor(frozen, recommendation.Lane, lane.Weights))
		// The recommendation is about the snapshot that carries the lane it
		// chose, so the frozen evidence and the proposal agree from here on.
		recommendation.SnapshotID = frozen.SnapshotID()
	}

	if err := savePending(ctx, opts, cfg, frozen, recommendation, attempt); err != nil {
		return writeResultError(stderr, "recommend", err)
	}

	if RenderingFor(stdout, opts.Inspect.ForceJSON) == RenderJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(map[string]any{
			"snapshot_id":    frozen.SnapshotID(),
			"recommendation": recommendation,
			"substituted":    outcome.Substituted,
			"text":           renderRecommendation(recommendation, frozen.Environment(), lane),
		}); err != nil {
			return writeResultError(stderr, "recommend", err)
		}
		return ExitOK
	}
	fmt.Fprint(stdout, renderRecommendation(recommendation, frozen.Environment(), lane))
	return ExitOK
}

// renderRecommendation picks A2-then-A5 or A3. There is no third rendering,
// because there is no third action.
func renderRecommendation(r assessment.Recommendation, environment string, lane config.Lane) string {
	if r.Action == assessment.ActionProceed {
		return assessment.RenderProceeding(r, environment, lane)
	}
	return assessment.RenderWaiting(r)
}

// patchFor is the proposal the chosen lane implies: the candidate image, and
// that lane's weights. Two things, which is all a Release Patch can ever be.
func patchFor(frozen delta.ReleaseDelta, lane string, weights []int) delta.Patch {
	patch := frozen.Deployment().Patch
	patch.Lane = lane
	patch.Weights = weights
	return patch
}

func configFor(opts InspectOptions, application string) (config.Config, error) {
	return config.Load(config.ForApp(opts.Home, application).File)
}

func readAssessment(opts RecommendOptions) ([]byte, error) {
	switch strings.TrimSpace(opts.AssessmentPath) {
	case "":
		return nil, release.Invalid("missing_assessment", "assessment",
			"no assessment was given",
			"Pass the path to the assessment, or - to read it from stdin.")
	case "-":
		if opts.Stdin == nil {
			return nil, release.Invalid("missing_assessment", "assessment",
				"nothing was piped in",
				"Pass the path to the assessment, or pipe it in.")
		}
		return io.ReadAll(opts.Stdin)
	default:
		raw, err := os.ReadFile(opts.AssessmentPath)
		if err != nil {
			return nil, release.Invalid("unreadable_assessment", "assessment",
				fmt.Sprintf("could not read the assessment: %v", err),
				"Check the path and try again.")
		}
		return raw, nil
	}
}

// savePending records the recommendation awaiting approval, together with the
// exact patch and the facts it was frozen against.
//
// There is at most one per Application and Environment, which is why nothing
// addresses it by identifier. Recording the facts here is what makes the
// pre-apply recheck a comparison rather than a re-derivation: `run` compares
// what is true now with what was true when a person read the recommendation.
func savePending(ctx context.Context, opts RecommendOptions, cfg config.Config,
	frozen delta.ReleaseDelta, recommendation assessment.Recommendation, attempt int) error {

	environment, _ := cfg.Environment(frozen.Environment())
	dir := config.ForApp(opts.Inspect.Home, frozen.Application()).ForEnvironment(frozen.Environment()).Dir

	pending := releasepatch.Pending{
		Application: frozen.Application(),
		Environment: frozen.Environment(),
		Snapshot:    frozen.SnapshotID(),
		Revision:    frozen.Candidate().Revision,
		Action:      string(recommendation.Action),
		Lane:        recommendation.Lane,
		At:          frozen.CapturedAt(),
		Attempts:    attempt,
	}
	deltaRaw, err := json.Marshal(frozen)
	if err != nil {
		return err
	}
	pending.Delta = deltaRaw
	recommendationRaw, err := json.Marshal(recommendation)
	if err != nil {
		return err
	}
	pending.Recommendation = recommendationRaw
	for _, objective := range frozen.Health() {
		pending.Analysis = append(pending.Analysis, string(objective.Name))
	}

	if recommendation.Action == assessment.ActionProceed {
		target, err := opts.Inspect.Cluster.Inspect(ctx, opts.Inspect.Root,
			environment.Kubernetes.Namespace, environment.Kubernetes.Rollout)
		if err != nil {
			return err
		}
		patch, err := releasepatch.Build(target.RolloutJSON, cfg.Artifact.Container,
			frozen.Deployment().Patch.Image, recommendation.Lane, frozen.Deployment().Patch.Weights)
		if err != nil {
			return err
		}
		container, _ := target.SelectedContainer(cfg.Artifact.Container)
		pending.Patch = patch
		pending.Facts = releasepatch.Facts{
			Revision:        frozen.Candidate().Revision,
			Digest:          frozen.Candidate().Digest,
			RunningImage:    container.Image,
			ConfigHash:      ConfigHash(opts.Inspect.Home, frozen.Application()),
			RolloutUID:      patch.RolloutUID,
			ResourceVersion: patch.ResourceVersion,
			PatchDigest:     patch.Digest(),
		}
	}
	return releasepatch.SavePending(dir, pending)
}
