package execute_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AndrewMaged814/safelane/internal/execute"
)

// canned Argo status JSON, per Appendix C5's mapping table. These are the
// seven rows of that table, expressed as the documents `kubectl get
// rollout -o json` would actually produce.
const (
	statusNotStarted  = `{"status":{}}`
	statusProgressing = `{"status":{"phase":"Progressing"},
		"spec":{"strategy":{"canary":{"steps":[{"setWeight":1},{"pause":{}}]}}}}`
	statusAtGate = `{"status":{"phase":"Paused","pauseConditions":[{"reason":"CanaryPauseStep"}],
		"currentStepIndex":1,"canary":{"weights":{"canary":{"weight":3}}}},
		"spec":{"strategy":{"canary":{"steps":[{"setWeight":1},{"pause":{}},{"setWeight":5},{"pause":{}}]}}}}`
	statusAnalysing = `{"status":{"phase":"Progressing","canary":{"currentStepAnalysisRunStatus":{"status":"Running"}}}}`
	statusComplete  = `{"status":{"phase":"Healthy","stableRS":"abc","currentPodHash":"abc"}}`
	statusDegraded  = `{"status":{"phase":"Degraded"}}`
	statusAborted   = `{"status":{"phase":"Degraded","abort":true}}`
)

func TestGetStatus_MapsEveryAppendixC5State(t *testing.T) {
	cases := []struct {
		name string
		json string
		want execute.State
	}{
		{"not_started", statusNotStarted, execute.StateNotStarted},
		{"progressing", statusProgressing, execute.StateProgressing},
		{"at_gate", statusAtGate, execute.StateAtGate},
		{"analysing", statusAnalysing, execute.StateAnalysing},
		{"complete", statusComplete, execute.StateComplete},
		{"degraded", statusDegraded, execute.StateDegraded},
		{"aborted", statusAborted, execute.StateAborted},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fr := &fakeRunner{}
			fr.enqueue(c.json, nil)
			ex := newTestExecutor(fr)
			st, err := ex.GetStatus(context.Background())
			if err != nil {
				t.Fatalf("GetStatus: %v", err)
			}
			if st.State != c.want {
				t.Errorf("state = %q, want %q", st.State, c.want)
			}
		})
	}
}

func TestGetStatus_CurrentWeight_PrefersTheObservedTrafficWeight(t *testing.T) {
	fr := &fakeRunner{}
	fr.enqueue(statusAtGate, nil)
	ex := newTestExecutor(fr)

	st, err := ex.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	// The fallback (last completed setWeight step) would compute 1 here;
	// the observed traffic weight (3) must win.
	if st.CurrentWeight != 3 {
		t.Errorf("current weight = %d, want 3 (from status.canary.weights.canary.weight, not the step fallback)", st.CurrentWeight)
	}
}

func TestGetStatus_ReportsGenerationAndArgoMessage(t *testing.T) {
	fr := &fakeRunner{}
	fr.enqueue(`{"metadata":{"generation":8,"annotations":{"safelane.dev/release-id":"rel_01ARZ3NDEKTSV4RRFFQ69G5FAV"}},"status":{"observedGeneration":"7","phase":"Degraded",`+
		`"abort":true,"message":"Rollout aborted update to revision 4"}}`, nil)
	ex := newTestExecutor(fr)

	st, err := ex.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.Generation != 8 || st.ObservedGeneration != 7 || st.Message != "Rollout aborted update to revision 4" {
		t.Fatalf("status diagnostics = %+v", st)
	}
	if st.ReleaseID != "rel_01ARZ3NDEKTSV4RRFFQ69G5FAV" {
		t.Fatalf("release annotation = %q", st.ReleaseID)
	}
}

func TestGetStatus_OnlyReportsRestoredAfterStableCapacityAndTrafficAreRestored(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want bool
	}{
		{
			name: "restored",
			doc:  `{"status":{"phase":"Degraded","abort":true,"stableRS":"stable","replicas":4,"readyReplicas":4,"updatedReplicas":0,"canary":{"weights":{"canary":{"weight":0}}}}}`,
			want: true,
		},
		{
			name: "canary traffic remains",
			doc:  `{"status":{"phase":"Degraded","abort":true,"stableRS":"stable","replicas":4,"readyReplicas":4,"updatedReplicas":0,"canary":{"weights":{"canary":{"weight":10}}}}}`,
		},
		{
			name: "updated replicas remain",
			doc:  `{"status":{"phase":"Degraded","abort":true,"stableRS":"stable","replicas":4,"readyReplicas":4,"updatedReplicas":1,"canary":{"weights":{"canary":{"weight":0}}}}}`,
		},
		{
			name: "stable capacity is not ready",
			doc:  `{"status":{"phase":"Degraded","abort":true,"stableRS":"stable","replicas":4,"readyReplicas":3,"updatedReplicas":0,"canary":{"weights":{"canary":{"weight":0}}}}}`,
		},
		{
			// The document a real rolled-back Rollout serves. Argo omits
			// updatedReplicas rather than serialising a 0, and leaves
			// currentPodHash on the rejected canary ReplicaSet.
			name: "restored, with the zero counter omitted as Argo omits it",
			doc:  `{"status":{"phase":"Degraded","abort":true,"stableRS":"86bf8c74db","currentPodHash":"854fd9b6c8","replicas":2,"readyReplicas":2,"availableReplicas":2,"canary":{}}}`,
			want: true,
		},
		{
			name: "not restored while a canary replica is still up, counter omitted",
			doc:  `{"status":{"phase":"Degraded","abort":true,"stableRS":"stable","replicas":4,"readyReplicas":3,"canary":{}}}`,
		},
		{
			name: "replica restoration evidence is absent",
			doc:  `{"status":{"phase":"Degraded","abort":true,"stableRS":"stable","canary":{"weights":{"canary":{"weight":0}}}}}`,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fr := &fakeRunner{}
			fr.enqueue(test.doc, nil)
			status, err := newTestExecutor(fr).GetStatus(context.Background())
			if err != nil {
				t.Fatalf("GetStatus: %v", err)
			}
			if status.Restored != test.want {
				t.Fatalf("Restored = %t, want %t", status.Restored, test.want)
			}
		})
	}
}

func TestGetStatus_CurrentWeight_FallsBackToTheLastCompletedStep(t *testing.T) {
	// No trafficRouting weight reported (no nginx/istio in play); fall
	// back to scanning steps up to currentStepIndex for the last
	// setWeight, per Appendix C5.
	doc := `{"status":{"phase":"Paused","pauseConditions":[{}],"currentStepIndex":2},
		"spec":{"strategy":{"canary":{"steps":[
			{"setWeight":5},{"pause":{}},{"setWeight":25},{"pause":{}}
		]}}}}`
	fr := &fakeRunner{}
	fr.enqueue(doc, nil)
	ex := newTestExecutor(fr)

	st, err := ex.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.CurrentWeight != 25 {
		t.Errorf("current weight = %d, want 25 (the last setWeight at or before currentStepIndex)", st.CurrentWeight)
	}
}

func TestWaitForGate_PollsUntilAtGateAndReportsTransitionsInOrder(t *testing.T) {
	fr := &fakeRunner{}
	fr.enqueue(statusNotStarted, nil)
	fr.enqueue(statusProgressing, nil)
	fr.enqueue(statusProgressing, nil) // same state again: must not re-fire onTransition
	fr.enqueue(statusAtGate, nil)
	ex := newTestExecutor(fr)

	var seen []execute.State
	st, err := ex.WaitForGate(context.Background(), time.Minute, func(s execute.Status) {
		seen = append(seen, s.State)
	})
	if err != nil {
		t.Fatalf("WaitForGate: %v", err)
	}
	if st.State != execute.StateAtGate {
		t.Errorf("final state = %q, want at_gate", st.State)
	}
	want := []execute.State{execute.StateNotStarted, execute.StateProgressing, execute.StateAtGate}
	if len(seen) != len(want) {
		t.Fatalf("transitions = %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Errorf("transition %d = %q, want %q", i, seen[i], want[i])
		}
	}
}

func TestWaitForGate_StopsAtDegradedAndAborted(t *testing.T) {
	for _, c := range []struct {
		name string
		json string
		want execute.State
	}{
		{"degraded", statusDegraded, execute.StateDegraded},
		{"aborted", statusAborted, execute.StateAborted},
	} {
		t.Run(c.name, func(t *testing.T) {
			fr := &fakeRunner{}
			fr.enqueue(c.json, nil)
			ex := newTestExecutor(fr)
			st, err := ex.WaitForGate(context.Background(), time.Minute, nil)
			if err != nil {
				t.Fatalf("WaitForGate: %v", err)
			}
			if st.State != c.want {
				t.Errorf("state = %q, want %q", st.State, c.want)
			}
		})
	}
}

func TestWaitForGate_TimesOutWithoutRetryingAnything(t *testing.T) {
	fr := &fakeRunner{}
	// Always progressing, never reaching a gate: the fake clock below
	// advances past the deadline without any real sleep.
	for i := 0; i < 5; i++ {
		fr.enqueue(statusProgressing, nil)
	}
	ex := newTestExecutor(fr)

	current := time.Date(2026, 8, 20, 14, 0, 0, 0, time.UTC)
	ex.Now = func() time.Time { return current }
	ex.Sleep = func(d time.Duration) { current = current.Add(d) }
	ex.PollInterval = 2 * time.Second

	_, err := ex.WaitForGate(context.Background(), 5*time.Second, nil)
	if !errors.Is(err, execute.ErrGateTimeout) {
		t.Fatalf("err = %v, want ErrGateTimeout", err)
	}
	// One kubectl call per poll; the timeout must never cause a second,
	// different kind of call (a promotion retry) to be issued.
	for _, call := range fr.calls {
		if call[0] != "get" {
			t.Errorf("unexpected call during a timed-out wait: %v", call)
		}
	}
}
