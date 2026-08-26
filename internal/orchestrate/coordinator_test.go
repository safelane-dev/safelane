package orchestrate_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AndrewMaged814/safelane/internal/journal"
	"github.com/AndrewMaged814/safelane/internal/orchestrate"
	"github.com/AndrewMaged814/safelane/internal/releasepatch"
)

type cluster struct {
	observed     []journal.Observed
	measurements []journal.Measurement
	applied      int
	promoted     int
}

func (c *cluster) ApplyPatch(context.Context, releasepatch.Patch) error {
	c.applied++
	return nil
}

func (c *cluster) ObserveRelease(context.Context) (journal.Observed, error) {
	if len(c.observed) == 0 {
		return journal.Observed{}, errors.New("test exhausted rollout observations")
	}
	next := c.observed[0]
	c.observed = c.observed[1:]
	return next, nil
}

func (c *cluster) Measurement(context.Context) (journal.Measurement, error) {
	if len(c.measurements) == 0 {
		return journal.MissingMeasurement(), nil
	}
	next := c.measurements[0]
	c.measurements = c.measurements[1:]
	return next, nil
}

func (c *cluster) Promote(context.Context) error {
	c.promoted++
	return nil
}

func releaseRecord() journal.Record {
	return journal.Record{
		ID: "payments-api-production-1", Application: "payments-api", Environment: "production",
		Candidate: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Lane: "standard",
		Attempt: 1, Started: time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC),
	}
}

func TestCoordinatorStaysAttachedAndPromotesAfterFreshMeasurements(t *testing.T) {
	store := journal.Store{Dir: t.TempDir()}
	cluster := &cluster{
		observed: []journal.Observed{
			{State: journal.StateMonitoring, Weight: 25, AtGate: true},
			{State: journal.StateMonitoring, Weight: 50, AtGate: true},
			{State: journal.StateCompleted, Weight: 100},
		},
		measurements: []journal.Measurement{
			{Phase: "Running", Successful: 1, Count: 1},
			{Phase: "Running", Successful: 2, Count: 2},
		},
	}
	clock := time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC)
	coordinator := orchestrate.Coordinator{
		Cluster: cluster, Store: store,
		Now:   func() time.Time { clock = clock.Add(time.Second); return clock },
		Sleep: func(time.Duration) {},
	}

	finished, err := coordinator.Run(context.Background(), releaseRecord(), releasepatch.Patch{})
	if err != nil {
		t.Fatal(err)
	}
	if cluster.applied != 1 || cluster.promoted != 2 {
		t.Fatalf("applied=%d promoted=%d", cluster.applied, cluster.promoted)
	}
	if finished.State != journal.StateCompleted || finished.Weight != 100 {
		t.Fatalf("finished = %+v", finished)
	}
	if _, found, err := store.Active(); err != nil || found {
		t.Fatalf("active after completion = %v, %v", found, err)
	}
	cards, err := store.History(10)
	if err != nil || len(cards) != 1 || cards[0].Outcome != "released" {
		t.Fatalf("history = %+v, %v", cards, err)
	}
}

func TestCoordinatorReportsLiveExposureWhileItStaysAttached(t *testing.T) {
	store := journal.Store{Dir: t.TempDir()}
	cluster := &cluster{
		observed: []journal.Observed{
			{State: journal.StateMonitoring, Weight: 25, AtGate: true},
			{State: journal.StateCompleted, Weight: 50},
		},
		measurements: []journal.Measurement{{Phase: "Running", Successful: 1, Count: 1}},
	}
	var steps []orchestrate.Step
	coordinator := orchestrate.Coordinator{
		Cluster: cluster, Store: store, Now: time.Now, Sleep: func(time.Duration) {},
		Progress: func(step orchestrate.Step) { steps = append(steps, step) },
	}

	finished, err := coordinator.Run(context.Background(), releaseRecord(), releasepatch.Patch{})
	if err != nil {
		t.Fatal(err)
	}
	if finished.Weight != 100 {
		t.Fatalf("completed weight = %d, want 100", finished.Weight)
	}
	var saw25, saw100 bool
	for _, step := range steps {
		if step.Weight == 25 {
			saw25 = true
		}
		if step.State == journal.StateCompleted && step.Weight == 100 {
			saw100 = true
		}
	}
	if !saw25 || !saw100 {
		t.Fatalf("progress = %+v, want 25%% gate and completed 100%%", steps)
	}
}

func TestCoordinatorNeverPromotesOnAnOldMeasurement(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := journal.Store{Dir: t.TempDir()}
	cluster := &cluster{
		observed:     []journal.Observed{{State: journal.StateMonitoring, Weight: 25, AtGate: true}},
		measurements: []journal.Measurement{{Phase: "Running", Successful: 0, Count: 1}},
	}
	coordinator := orchestrate.Coordinator{
		Cluster: cluster, Store: store, Now: time.Now,
		Sleep: func(time.Duration) { cancel() },
	}

	_, err := coordinator.Run(ctx, releaseRecord(), releasepatch.Patch{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if cluster.promoted != 0 {
		t.Fatalf("promoted %d times without a fresh measurement", cluster.promoted)
	}
	active, found, loadErr := store.Active()
	if loadErr != nil || !found || active.State != journal.StateMonitoring {
		t.Fatalf("active = %+v, %v, %v", active, found, loadErr)
	}

	cluster.observed = []journal.Observed{{State: journal.StateCompleted, Weight: 100}}
	resumed, err := coordinator.Run(context.Background(), releaseRecord(), releasepatch.Patch{})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed.State != journal.StateCompleted || cluster.applied != 1 {
		t.Fatalf("resume reapplied or did not finish: applied=%d record=%+v", cluster.applied, resumed)
	}
}

func TestCoordinatorRecordsArgoAnalysisFailureWithoutIssuingAbort(t *testing.T) {
	store := journal.Store{Dir: t.TempDir()}
	cluster := &cluster{
		observed: []journal.Observed{
			{State: journal.StateMonitoring, Weight: 25, AtGate: true},
			{State: journal.StateMonitoring, Weight: 25, AtGate: true},
			{State: journal.StateFailed, Weight: 25, Restored: false},
			{State: journal.StateFailed, Weight: 0, Restored: true},
		},
		measurements: []journal.Measurement{{Phase: "Failed", Successful: 0, Count: 1}},
	}
	coordinator := orchestrate.Coordinator{Cluster: cluster, Store: store, Now: time.Now, Sleep: func(time.Duration) {}}

	finished, err := coordinator.Run(context.Background(), releaseRecord(), releasepatch.Patch{})
	if err != nil {
		t.Fatal(err)
	}
	if cluster.promoted != 0 || finished.State != journal.StateFailed {
		t.Fatalf("promoted=%d finished=%+v", cluster.promoted, finished)
	}
	if len(cluster.observed) != 0 {
		t.Fatalf("coordinator detached before Argo's terminal state: %d observations remain", len(cluster.observed))
	}
	if len(finished.Events) == 0 || finished.Events[len(finished.Events)-1].By != journal.ActorArgo {
		t.Fatalf("failure attribution = %+v", finished.Events)
	}
}

func TestCoordinatorReportsEachMovementInExposureOnce(t *testing.T) {
	store := journal.Store{Dir: t.TempDir()}
	cluster := &cluster{
		observed: []journal.Observed{
			// 25 twice: the second is a poll that saw no movement, and it must
			// not produce a second line, or a slow analysis would bury the
			// release in repetition.
			{State: journal.StateMonitoring, Weight: 25, AtGate: true},
			{State: journal.StateMonitoring, Weight: 25, AtGate: true},
			{State: journal.StateMonitoring, Weight: 50, AtGate: true},
			{State: journal.StateCompleted, Weight: 100},
		},
		measurements: []journal.Measurement{
			{Phase: "Running", Successful: 1, Count: 1},
			{Phase: "Running", Successful: 2, Count: 2},
			{Phase: "Running", Successful: 3, Count: 3},
		},
	}
	clock := time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC)
	var steps []orchestrate.Step
	coordinator := orchestrate.Coordinator{
		Cluster: cluster, Store: store,
		Now:      func() time.Time { clock = clock.Add(time.Second); return clock },
		Sleep:    func(time.Duration) {},
		Progress: func(step orchestrate.Step) { steps = append(steps, step) },
	}

	if _, err := coordinator.Run(context.Background(), releaseRecord(), releasepatch.Patch{}); err != nil {
		t.Fatal(err)
	}

	var weights []int
	for _, step := range steps[1:] { // steps[0] is the apply
		weights = append(weights, step.Weight)
	}
	want := []int{25, 50, 100}
	if len(weights) != len(want) {
		t.Fatalf("reported weights = %v, want %v", weights, want)
	}
	for i := range want {
		if weights[i] != want[i] {
			t.Fatalf("reported weights = %v, want %v", weights, want)
		}
	}
	if steps[0].Detail == "" {
		t.Fatal("the apply should be reported, so the wait is not silent from the first second")
	}
}

func TestCoordinatorReportsNothingWhenNoProgressIsWanted(t *testing.T) {
	store := journal.Store{Dir: t.TempDir()}
	cluster := &cluster{
		observed:     []journal.Observed{{State: journal.StateCompleted, Weight: 100}},
		measurements: []journal.Measurement{},
	}
	clock := time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC)
	coordinator := orchestrate.Coordinator{
		Cluster: cluster, Store: store,
		Now:   func() time.Time { clock = clock.Add(time.Second); return clock },
		Sleep: func(time.Duration) {},
	}

	if _, err := coordinator.Run(context.Background(), releaseRecord(), releasepatch.Patch{}); err != nil {
		t.Fatal(err)
	}
}
