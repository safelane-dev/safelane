package journal_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/AndrewMaged814/safelane/internal/journal"
	"github.com/AndrewMaged814/safelane/internal/release"
)

func at(minute int) time.Time {
	return time.Date(2026, 8, 21, 14, minute, 0, 0, time.UTC)
}

func store(t *testing.T) journal.Store {
	t.Helper()
	return journal.Store{Dir: t.TempDir()}
}

func started(t *testing.T, s journal.Store) journal.Record {
	t.Helper()
	record, err := s.Start(journal.Record{
		ID:          journal.NewID("payments-api", "production", 1, at(0)),
		Application: "payments-api",
		Environment: "production",
		Candidate:   strings.Repeat("a", 40),
		Lane:        "standard",
		State:       journal.StateApplying,
		Started:     at(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	return record
}

// One active release per Application and Environment. A second would mean a
// person could approve one recommendation and run the other, and no command
// takes an identifier that would let them tell which.
func TestOnlyOneReleaseIsActivePerPair(t *testing.T) {
	s := store(t)
	first := started(t, s)

	_, err := s.Start(journal.Record{
		ID: "another", Application: "payments-api", Environment: "production",
		State: journal.StateApplying, Started: at(1),
	})
	assertRejection(t, err, "release_already_active")

	active, found, err := s.Active()
	if err != nil || !found {
		t.Fatalf("Active = %v %v", found, err)
	}
	if active.ID != first.ID {
		t.Errorf("active = %q", active.ID)
	}
}

// Every command resolves the active release from the pair. Nothing passes an
// identifier.
func TestTheActiveReleaseResolvesWithoutAnIdentifier(t *testing.T) {
	s := store(t)
	record := started(t, s)

	active, found, err := s.Active()
	if err != nil || !found {
		t.Fatalf("Active = %v %v", found, err)
	}
	if active.Candidate != record.Candidate || active.Lane != "standard" {
		t.Errorf("active = %+v", active)
	}
}

// Reconnecting is a read, not a replay: the same lookup twice gives the same
// answer and changes nothing.
func TestReconnectingIsIdempotent(t *testing.T) {
	s := store(t)
	started(t, s)

	first, _, err := s.Active()
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := s.Active()
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Events) != len(second.Events) {
		t.Errorf("reading the active release changed it: %d then %d events", len(first.Events), len(second.Events))
	}
}

// A retry is a new attempt. Reopening the old one would mean the evidence,
// recommendation and approval that were true for the first attempt now
// describe the second, and none of them do.
func TestRetryingCreatesANewImmutableAttempt(t *testing.T) {
	s := store(t)
	record := started(t, s)
	finished, err := s.Finish(record, journal.StateFailed, "argo stopped the rollout", "", at(10))
	if err != nil {
		t.Fatal(err)
	}

	retry, err := s.Retry(finished, at(20))
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if retry.ID == finished.ID {
		t.Error("the retry reused the previous identifier")
	}
	if retry.Attempt != 2 || retry.Previous != finished.ID {
		t.Errorf("retry = %+v", retry)
	}

	// The first attempt is still exactly as it ended.
	original, found, err := s.Load(finished.ID)
	if err != nil || !found {
		t.Fatalf("Load = %v %v", found, err)
	}
	if original.State != journal.StateFailed || original.Outcome != "argo stopped the rollout" {
		t.Errorf("the previous attempt changed: %+v", original)
	}
}

// The card is derived from the record, so the compact history and the detailed
// proof cannot disagree: there is only one place the answer comes from.
func TestCompactHistoryAndDetailedProofAgree(t *testing.T) {
	s := store(t)
	record := started(t, s)
	record.Reason = "the analysis stayed healthy"
	finished, err := s.Finish(record, journal.StateCompleted, "released at 100%", "", at(30))
	if err != nil {
		t.Fatal(err)
	}

	cards, err := s.History(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 {
		t.Fatalf("cards = %d", len(cards))
	}
	card := cards[0]
	if card.Candidate != finished.Candidate || card.Lane != finished.Lane ||
		card.Outcome != finished.Outcome || card.Reason != finished.Reason {
		t.Errorf("the card disagrees with the record:\ncard   %+v\nrecord %+v", card, finished)
	}
	if card.Recommendation != "proceed" {
		t.Errorf("recommendation = %q", card.Recommendation)
	}
	if !card.At.Equal(finished.Ended) {
		t.Errorf("card at %s, record ended %s", card.At, finished.Ended)
	}
}

// Finishing releases the active slot, so the next release can start.
func TestFinishingFreesTheActiveSlot(t *testing.T) {
	s := store(t)
	record := started(t, s)
	if _, err := s.Finish(record, journal.StateCompleted, "released at 100%", "", at(10)); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.Active(); err != nil || found {
		t.Fatalf("Active = %v %v after finishing", found, err)
	}
	if _, err := s.Start(journal.Record{
		ID: "next", Application: "payments-api", Environment: "production",
		State: journal.StateAssessing, Started: at(20),
	}); err != nil {
		t.Fatalf("Start after finishing: %v", err)
	}
}

// The normal evidence view is meant to be cheap. Older history is still on
// disk and still readable; it loads when somebody has a concrete question.
func TestHistoryIsBoundedToTheNewestCards(t *testing.T) {
	s := store(t)
	for i := 0; i < 15; i++ {
		record, err := s.Start(journal.Record{
			ID:          journal.NewID("payments-api", "production", i+1, at(i)),
			Application: "payments-api", Environment: "production",
			Candidate: strings.Repeat("a", 39) + string(rune('a'+i%26)),
			State:     journal.StateApplying, Started: at(i),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Finish(record, journal.StateCompleted, "released", "", at(i)); err != nil {
			t.Fatal(err)
		}
	}

	bounded, err := s.History(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(bounded) != 10 {
		t.Fatalf("bounded history = %d cards", len(bounded))
	}
	if !bounded[0].At.Equal(at(14)) {
		t.Errorf("newest card = %s", bounded[0].At)
	}

	// Everything is still there for a concrete question.
	all, err := s.History(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 15 {
		t.Errorf("full history = %d cards", len(all))
	}
}

// The record holds the evidence, the recommendation, the patch and what
// happened. Not the conversation that surrounded any of it.
func TestARecordStoresNoConversationDraftsOrTraces(t *testing.T) {
	s := store(t)
	record := started(t, s)
	record.Delta = json.RawMessage(`{"snapshot_id":"sha256:abc"}`)
	record.Recommendation = json.RawMessage(`{"action":"proceed"}`)
	record.Patch = json.RawMessage(`{"operations":[]}`)
	if err := s.Save(record); err != nil {
		t.Fatal(err)
	}

	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}

	allowed := map[string]bool{
		"id": true, "application": true, "environment": true, "candidate": true,
		"lane": true, "state": true, "attempt": true, "previous": true,
		"delta": true, "recommendation": true, "patch": true, "approval": true,
		"events": true, "weight": true, "reason": true, "outcome": true,
		"started": true, "ended": true,
	}
	for key := range fields {
		if !allowed[key] {
			t.Errorf("a release record carries an unexpected field %q", key)
		}
	}
	for _, forbidden := range []string{"conversation", "messages", "transcript", "draft", "trace", "prompt"} {
		if _, present := fields[forbidden]; present {
			t.Errorf("a release record carries %q", forbidden)
		}
	}
}

func assertRejection(t *testing.T, err error, code string) {
	t.Helper()
	for _, e := range release.Flatten(err) {
		if e.Code == code {
			return
		}
	}
	t.Errorf("want a rejection with code %q, got %v", code, err)
}
