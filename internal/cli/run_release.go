package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/AndrewMaged814/safelane/internal/assessment"
	"github.com/AndrewMaged814/safelane/internal/config"
	"github.com/AndrewMaged814/safelane/internal/releasepatch"
)

// RunOptions are everything `safelane run <env>` needs.
type RunOptions struct {
	Inspect InspectOptions
	// Confirm reads the answer to the approval question at a terminal. Nil
	// means there is nobody to ask, which is the piped case.
	Confirm io.Reader
	// Apply performs the mutation. It is a port so this ticket can prove the
	// approval gate without owning execution, which is ticket 09's.
	Apply func(ctx context.Context, patch releasepatch.Patch) error
	Now   func() time.Time
}

// Run releases what is awaiting approval, and nothing else.
//
// The gate is the whole of this function: there must be a pending proceeding
// recommendation, the facts it was frozen against must still hold, and the
// approval must be unspent. Only then is the cluster touched, and the approval
// is spent in the same breath.
//
// `run` never assesses. Assessing and approving are separate acts on purpose,
// and a `run` that quietly assessed first would collapse them - the person
// would be approving something they had not read.
func Run(ctx context.Context, opts RunOptions, stdout, stderr io.Writer) int {
	application, err := applicationFrom(opts.Inspect.Root, opts.Inspect.Home, opts.Inspect.App, opts.Inspect.Cluster.Origin)
	if err != nil {
		return writeResultError(stderr, "run", err)
	}
	cfg, err := config.Load(config.ForApp(opts.Inspect.Home, application).File)
	if err != nil {
		return writeResultError(stderr, "run", err)
	}
	environment, ok := cfg.Environment(opts.Inspect.Environment)
	if !ok {
		return writeResultError(stderr, "run", unknownEnvironment(application, opts.Inspect.Environment, cfg))
	}
	dir := config.ForApp(opts.Inspect.Home, application).ForEnvironment(environment.Name).Dir

	pending, found, err := releasepatch.LoadPending(dir)
	if err != nil {
		return writeResultError(stderr, "run", err)
	}
	if !found {
		return writeResultError(stderr, "run", releasepatch.NothingAwaitingApproval(application, environment.Name))
	}
	if pending.Action != string(assessment.ActionProceed) {
		return writeResultError(stderr, "run", releasepatch.WaitingCannotRun(application, environment.Name))
	}

	// At a terminal, ask. Piped, the frozen recommendation is the
	// authorization and nothing is asked: a flag the caller passes on every
	// invocation is not a safety mechanism.
	if opts.Confirm != nil && RenderingFor(stdout, opts.Inspect.ForceJSON) == RenderText {
		fmt.Fprint(stdout, assessment.RenderApprovalQuestion(environment.Name))
		answer, readErr := bufio.NewReader(opts.Confirm).ReadString('\n')
		if readErr != nil && strings.TrimSpace(answer) == "" {
			return writeResultError(stderr, "run", refused(environment.Name))
		}
		// The question has just been asked, so a bare "yes" is the answer to
		// it. Anywhere else it would not be.
		if !releasepatch.IsApproval(answer, true) {
			fmt.Fprintf(stdout, "\nNothing was changed.\n")
			return ExitOK
		}
	}

	now := opts.now()
	approval := pending.Approval
	if approval == nil {
		granted, grantErr := releasepatch.Grant(application, environment.Name,
			pending.Snapshot, pending.Revision, pending.Patch, pending.Analysis, true, now)
		if grantErr != nil {
			return writeResultError(stderr, "run", grantErr)
		}
		approval = &granted
	}

	// The recheck is immediately before the cluster is touched, not at
	// approval time. The gap between the two is exactly where a digest moves.
	current, err := currentFacts(ctx, opts, cfg, application, environment, pending)
	if err != nil {
		return writeResultError(stderr, "run", err)
	}
	rechecked, err := approval.Recheck(pending.Facts, current, now)
	if err != nil {
		pending.Approval = &rechecked
		_ = releasepatch.SavePending(dir, pending)
		return writeResultError(stderr, "run", err)
	}

	spent, err := rechecked.Use(now)
	if err != nil {
		return writeResultError(stderr, "run", err)
	}
	pending.Approval = &spent
	if err := releasepatch.SavePending(dir, pending); err != nil {
		return writeResultError(stderr, "run", err)
	}

	// Nil means the real cluster. A test substitutes it; production does not
	// have to remember to pass it.
	apply := opts.Apply
	if apply == nil {
		apply = Cluster{Home: opts.Inspect.Home, Application: application, Environment: environment}.ApplyPatch
	}
	if err := apply(ctx, pending.Patch); err != nil {
		return writeResultError(stderr, "run", err)
	}

	if RenderingFor(stdout, opts.Inspect.ForceJSON) == RenderJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		_ = encoder.Encode(map[string]any{
			"application": application,
			"environment": environment.Name,
			"snapshot":    pending.Snapshot,
			"lane":        pending.Lane,
			"approval":    spent,
			"patch":       pending.Patch,
		})
		return ExitOK
	}
	fmt.Fprintf(stdout, "Releasing %s to %s in the %s lane.\n", application, environment.Name, pending.Lane)
	return ExitOK
}

func (o RunOptions) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now().UTC()
}

// currentFacts re-reads the material facts. It goes back to the cluster and the
// registry rather than trusting anything cached, because the point of a recheck
// is to catch what moved while nobody was looking.
func currentFacts(ctx context.Context, opts RunOptions, cfg config.Config,
	application string, environment config.Environment, pending releasepatch.Pending) (releasepatch.Facts, error) {

	frozen, _, err := FreezeDelta(ctx, opts.Inspect)
	if err != nil {
		return releasepatch.Facts{}, err
	}
	target, err := opts.Inspect.Cluster.Inspect(ctx, opts.Inspect.Root,
		environment.Kubernetes.Namespace, environment.Kubernetes.Rollout)
	if err != nil {
		return releasepatch.Facts{}, err
	}
	container, _ := target.SelectedContainer(cfg.Artifact.Container)

	// Rebuilt with the lane a person actually approved, not with the default
	// the evidence was frozen against. Comparing the approved patch with a
	// patch for a different lane would cancel every approval.
	patch, err := releasepatch.Build(target.RolloutJSON, cfg.Artifact.Container,
		frozen.Deployment().Patch.Image, pending.Lane, cfg.Policy.Lanes[pending.Lane].Weights)
	if err != nil {
		return releasepatch.Facts{}, err
	}
	return releasepatch.Facts{
		Revision:        frozen.Candidate().Revision,
		Digest:          frozen.Candidate().Digest,
		RunningImage:    container.Image,
		ConfigHash:      ConfigHash(opts.Inspect.Home, application),
		RolloutUID:      patch.RolloutUID,
		ResourceVersion: patch.ResourceVersion,
		PatchDigest:     patch.Digest(),
	}, nil
}

func refused(environment string) error {
	return fmt.Errorf("nothing was changed; no answer was given to the rollout question for %s", environment)
}
