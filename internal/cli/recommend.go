package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/AndrewMaged814/safelane/internal/assessment"
	"github.com/AndrewMaged814/safelane/internal/config"
	"github.com/AndrewMaged814/safelane/internal/delta"
	"github.com/AndrewMaged814/safelane/internal/release"
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

	attempt := opts.Attempt
	if attempt < 1 {
		attempt = 1
	}
	outcome := assessment.Resolve(raw, frozen, cfg.Policy, attempt)
	if outcome.Correction != nil {
		fmt.Fprint(stderr, assessment.CorrectionRequest(outcome.Correction))
		return ExitDecision
	}

	recommendation := outcome.Recommendation
	lane := config.Lane{}
	if recommendation.Action == assessment.ActionProceed {
		_, lane, _ = cfg.Policy.LaneFor(config.Risk(recommendation.Risk))
		frozen = frozen.WithPatch(patchFor(frozen, recommendation.Lane, lane.Weights))
		// The recommendation is about the snapshot that carries the lane it
		// chose, so the frozen evidence and the proposal agree from here on.
		recommendation.SnapshotID = frozen.SnapshotID()
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
