// Package orchestrate owns the attached release loop.
//
// Argo executes traffic movement and health analysis. The Coordinator keeps
// one Release Attempt attached to that execution, asks for the next promotion
// only after a fresh successful background measurement, and closes the proof
// when Argo reaches a terminal outcome.
package orchestrate

import (
	"context"
	"fmt"
	"time"

	"github.com/AndrewMaged814/safelane/internal/journal"
	"github.com/AndrewMaged814/safelane/internal/releasepatch"
)

// Cluster is the narrow runtime boundary used by the release loop.
type Cluster interface {
	ApplyPatch(context.Context, releasepatch.Patch) error
	ObserveRelease(context.Context) (journal.Observed, error)
	Measurement(context.Context) (journal.Measurement, error)
	Promote(context.Context) error
}

// Coordinator runs one approved Release Attempt to a terminal outcome or
// until the caller's context ends. A context cancellation leaves the durable
// attempt active so a later run or status call can reconnect to it.
type Coordinator struct {
	Cluster      Cluster
	Store        journal.Store
	Now          func() time.Time
	Sleep        func(time.Duration)
	PollInterval time.Duration
	// Progress reports a change worth showing someone waiting at a terminal.
	// It is called when the exposure moves, not once per poll, so a rollout
	// sitting in analysis stays quiet instead of repeating itself. Nil means
	// the caller does not want progress, which is how every non-interactive
	// caller and every test behaves.
	Progress func(Step)
}

// Step is one reportable movement in a release.
type Step struct {
	At     time.Time
	State  journal.State
	Weight int
	Detail string
}

func (c Coordinator) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func (c Coordinator) report(state journal.State, weight int, detail string) {
	if c.Progress == nil {
		return
	}
	c.Progress(Step{At: c.now(), State: state, Weight: weight, Detail: detail})
}

func (c Coordinator) sleep() {
	delay := c.PollInterval
	if delay <= 0 {
		delay = 2 * time.Second
	}
	if c.Sleep != nil {
		c.Sleep(delay)
		return
	}
	time.Sleep(delay)
}

// Run applies the approved patch once and stays attached. It never aborts in
// response to analysis failure; Argo owns that decision and SafeLane records
// it with Argo as the actor.
func (c Coordinator) Run(ctx context.Context, record journal.Record, patch releasepatch.Patch) (journal.Record, error) {
	if err := ctx.Err(); err != nil {
		return record, err
	}
	active, found, err := c.Store.Active()
	if err != nil {
		return record, err
	}
	if found {
		if active.ID != record.ID {
			return record, fmt.Errorf("active release %s does not match %s", active.ID, record.ID)
		}
		record = active
	} else {
		record.State = journal.StateApplying
		record.Events = append(record.Events, journal.Event{
			At: c.now(), Kind: "applying", By: journal.ActorSafeLane,
			Detail: "applying the approved image and canary steps",
		})
		record, err = c.Store.Start(record)
		if err != nil {
			return record, err
		}

		if err := c.Cluster.ApplyPatch(ctx, patch); err != nil {
			record, _ = c.Store.Append(record, journal.Event{
				At: c.now(), Kind: "apply_failed", By: journal.ActorSafeLane, Detail: err.Error(),
			})
			finished, finishErr := c.Store.Finish(record, journal.StateFailed, "apply failed", err.Error(), c.now())
			if finishErr != nil {
				return record, finishErr
			}
			return finished, err
		}
		record.State = journal.StateMonitoring
		record, err = c.Store.Append(record, journal.Event{
			At: c.now(), Kind: "patch_applied", By: journal.ActorSafeLane,
			Detail: "the approved image and canary steps were applied",
		})
		if err != nil {
			return record, err
		}
		c.report(record.State, record.Weight, "the approved image and canary steps were applied")
	}

	// -1 rather than 0 so an opening observation of 0% is still reported: 0 is a
	// real exposure a caller should see, not the absence of one.
	reportedWeight := -1

	for {
		if err := ctx.Err(); err != nil {
			return record, err
		}
		current, found, err := c.Store.Load(record.ID)
		if err != nil {
			return record, err
		}
		if !found {
			return record, fmt.Errorf("active release record %s disappeared", record.ID)
		}
		record = current
		if record.State.Terminal() {
			return record, nil
		}
		progressionStopped := hasEvent(record.Events, "analysis_failed")
		if record.State == journal.StatePaused {
			c.sleep()
			continue
		}

		observed, err := c.Cluster.ObserveRelease(ctx)
		if err != nil {
			return record, err
		}
		record.Weight = observed.Weight
		if observed.Weight != reportedWeight {
			c.report(observed.State, observed.Weight, "")
			reportedWeight = observed.Weight
		}

		switch observed.State {
		case journal.StateCompleted:
			record, err = c.Store.Append(record, journal.Event{
				At: c.now(), Kind: "completed", By: journal.ActorArgo,
				Detail: "Argo completed the rollout", Weight: observed.Weight,
			})
			if err != nil {
				return record, err
			}
			return c.Store.Finish(record, journal.StateCompleted, "released", "", c.now())
		case journal.StateFailed:
			if !observed.Restored {
				record.State = journal.StateMonitoring
				if err := c.Store.Save(record); err != nil {
					return record, err
				}
				c.sleep()
				continue
			}
			if hasEvent(record.Events, "stop") {
				record, err = c.Store.Append(record, journal.Event{
					At: c.now(), Kind: "restored", By: journal.ActorArgo,
					Detail: "Argo restored the stable version after the requested stop", Weight: observed.Weight,
				})
				if err != nil {
					return record, err
				}
				return c.Store.Finish(record, journal.StateStopped,
					"stopped at your request; stable version restored", record.Reason, c.now())
			}
			record, err = c.Store.Append(record, journal.Event{
				At: c.now(), Kind: "failed", By: journal.ActorArgo,
				Detail: "Argo stopped the rollout and restored the stable version", Weight: observed.Weight,
			})
			if err != nil {
				return record, err
			}
			return c.Store.Finish(record, journal.StateFailed, "Argo restored the stable version", "", c.now())
		}

		record.State = journal.StateMonitoring
		if err := c.Store.Save(record); err != nil {
			return record, err
		}
		if !observed.AtGate {
			c.sleep()
			continue
		}
		if progressionStopped {
			// The analysis has ended progression, but only the Rollout can say
			// restoration is complete. Stay attached without measuring again or
			// requesting another promotion until Argo reports a terminal state.
			c.sleep()
			continue
		}

		measurement, err := c.Cluster.Measurement(ctx)
		if err != nil {
			return record, err
		}
		gate := journal.Gate{SuccessfulAtLastGate: record.SuccessfulAtLastGate}
		decision := gate.Decide(measurement)
		if decision.Stop {
			record.Reason = decision.Reason
			record, err = c.Store.Append(record, journal.Event{
				At: c.now(), Kind: "analysis_failed", By: decision.By,
				Detail: decision.Reason, Weight: observed.Weight,
			})
			if err != nil {
				return record, err
			}
			c.sleep()
			continue
		}
		if !decision.Promote {
			c.sleep()
			continue
		}
		if err := c.Cluster.Promote(ctx); err != nil {
			return record, err
		}
		record.SuccessfulAtLastGate = measurement.Successful
		record, err = c.Store.Append(record, journal.Event{
			At: c.now(), Kind: "promoted", By: decision.By,
			Detail: decision.Reason, Weight: observed.Weight,
		})
		if err != nil {
			return record, err
		}
	}
}

func hasEvent(events []journal.Event, kind string) bool {
	for _, event := range events {
		if event.Kind == kind {
			return true
		}
	}
	return false
}
