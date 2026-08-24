package cli

import (
	"context"
	"strings"

	"github.com/AndrewMaged814/safelane/internal/config"
	"github.com/AndrewMaged814/safelane/internal/execute"
	"github.com/AndrewMaged814/safelane/internal/journal"
	"github.com/AndrewMaged814/safelane/internal/releasepatch"
)

// Cluster is what SafeLane does to a Rollout, as opposed to what it reads.
//
// Every call here goes through the controller identity, whose RBAC allows
// `get` and `patch` on one named Rollout and `get` on AnalysisRuns. That is
// the boundary: SafeLane cannot create a Service, rewrite an AnalysisTemplate,
// or touch a second Rollout, and it cannot do so even if this code tried.
type Cluster struct {
	Home        string
	Application string
	Environment config.Environment
}

// executor builds a controller-credentialled executor for one Environment.
//
// The controller kubeconfig path is derived from the Application and
// Environment names - it is never read from YAML, because a path in a settings
// file is a path somebody can repoint.
func (c Cluster) executor() *execute.Executor {
	locations := config.ForApp(c.Home, c.Application).ForEnvironment(c.Environment.Name)
	return execute.New(execute.Config{
		Namespace:            c.Environment.Kubernetes.Namespace,
		Rollout:              c.Environment.Kubernetes.Rollout,
		ControllerKubeconfig: locations.ControllerKubeconfig,
		ControllerContext:    "safelane-controller",
	})
}

// ApplyPatch sends the approved Release Patch.
func (c Cluster) ApplyPatch(ctx context.Context, patch releasepatch.Patch) error {
	body, err := patch.JSON()
	if err != nil {
		return err
	}
	return c.executor().ApplyPatch(ctx, body)
}

// Control asks Argo to hold, continue or stop.
//
// Argo is the authority on all three. SafeLane asks; it does not reimplement
// pausing, and it certainly does not reimplement restoring the stable version.
func (c Cluster) Control(ctx context.Context, action string, _ config.Environment) error {
	executor := c.executor()
	switch action {
	case "hold":
		return executor.Pause(ctx)
	case "continue":
		return executor.Promote(ctx)
	case "stop":
		return executor.Abort(ctx)
	}
	return nil
}

// Observe reads what the Rollout currently says, so a stored record can be
// reconciled against it.
func (c Cluster) Observe(ctx context.Context, _ config.Environment) (journal.Observed, error) {
	return c.ObserveRelease(ctx)
}

// ObserveRelease is the attached coordinator's read of the Rollout.
func (c Cluster) ObserveRelease(ctx context.Context) (journal.Observed, error) {
	status, err := c.executor().GetStatus(ctx)
	if err != nil {
		return journal.Observed{}, err
	}
	return journal.Observed{
		State:   observedState(status),
		Weight:  status.CurrentWeight,
		AtGate:  status.State == execute.StateAtGate,
		Aborted: status.State == execute.StateAborted,
	}, nil
}

// Promote requests only the next Argo progression step.
func (c Cluster) Promote(ctx context.Context) error { return c.executor().Promote(ctx) }

// observedState maps Argo's own read of the Rollout onto SafeLane's eight
// states.
//
// Only the ones the cluster can actually tell us about. `assessing` and
// `awaiting_approval` happen before anything reaches Kubernetes, and `stopped`
// is SafeLane's own record of a user's decision - Argo aborts the same way
// whether a person asked or the analysis failed, so reading `stopped` back out
// of the cluster would turn every user stop into a failure.
func observedState(status execute.Status) journal.State {
	switch status.State {
	case execute.StateComplete:
		return journal.StateCompleted
	case execute.StateAborted, execute.StateDegraded:
		return journal.StateFailed
	case execute.StateAtGate, execute.StateAnalysing:
		return journal.StateMonitoring
	case execute.StateProgressing:
		return journal.StateApplying
	}
	return journal.StateMonitoring
}

// Measurement reads the current background AnalysisRun, which is what the gate
// decides from.
//
// The name comes from the Rollout's own status rather than being constructed,
// because Argo chooses it. An empty name means no run is going yet, which is
// waiting - not a failure, and never a pass.
func (c Cluster) Measurement(ctx context.Context) (journal.Measurement, error) {
	executor := c.executor()
	status, err := executor.GetStatus(ctx)
	if err != nil {
		return journal.MissingMeasurement(), err
	}
	analysisRunName := status.AnalysisRunName
	if strings.TrimSpace(analysisRunName) == "" {
		// No AnalysisRun yet is not a failed one. It is waiting.
		return journal.MissingMeasurement(), nil
	}
	run, err := executor.GetAnalysisRun(ctx, analysisRunName)
	if err != nil {
		return journal.MissingMeasurement(), err
	}
	return journal.Measurement{
		Phase:      run.Phase,
		Successful: run.Metric.Successful,
		Count:      run.Metric.Count,
		Measured:   run.Metric.Measured,
	}, nil
}
