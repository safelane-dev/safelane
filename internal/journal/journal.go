package journal

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AndrewMaged814/safelane/internal/release"
)

// Actor is who did a thing. It matters because two of them are not SafeLane,
// and a record that said "SafeLane stopped the rollout" when Argo did would be
// claiming credit for somebody else's brake.
type Actor string

const (
	ActorSafeLane Actor = "safelane"
	ActorArgo     Actor = "argo"
	ActorUser     Actor = "user"
)

// Event is one thing that happened, in order. Events are append-only: a record
// that could be edited is a record that proves nothing.
type Event struct {
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"`
	By     Actor     `json:"by"`
	Detail string    `json:"detail,omitempty"`
	// Weight is the exposure at the time, when the event is about exposure.
	Weight int `json:"weight,omitempty"`
}

// Record is everything SafeLane kept about one release attempt.
//
// The fields are the whole list, and the list is short on purpose: the frozen
// evidence, the recommendation it produced, the patch that was approved, what
// happened, and how it ended. Nothing about the conversation that surrounded
// any of it.
type Record struct {
	ID          string `json:"id"`
	Application string `json:"application"`
	Environment string `json:"environment"`
	Candidate   string `json:"candidate"`
	Lane        string `json:"lane,omitempty"`
	State       State  `json:"state"`
	// Attempt is which try this is. A retry is a new record with the number
	// incremented, never the old one reopened.
	Attempt int `json:"attempt"`
	// Previous is the record this one retried, when it is a retry.
	Previous string `json:"previous,omitempty"`

	Delta          json.RawMessage `json:"delta,omitempty"`
	Recommendation json.RawMessage `json:"recommendation,omitempty"`
	Patch          json.RawMessage `json:"patch,omitempty"`
	Approval       json.RawMessage `json:"approval,omitempty"`

	Events []Event `json:"events"`
	Weight int     `json:"weight,omitempty"`
	// SuccessfulAtLastGate makes the "fresh measurement" rule durable across
	// process restarts. A reconnect must not reuse the reading that widened the
	// previous gate.
	SuccessfulAtLastGate int       `json:"successful_at_last_gate,omitempty"`
	Reason               string    `json:"reason,omitempty"`
	Outcome              string    `json:"outcome,omitempty"`
	Started              time.Time `json:"started"`
	Ended                time.Time `json:"ended,omitempty"`
}

// Status renders this record's line.
func (r Record) Status() Status {
	return Status{
		State: r.State, Environment: r.Environment,
		Weight: r.Weight, Reason: r.Reason, Since: r.Ended,
	}
}

// HistoryCard is the compact form: one line per release, enough to see a
// pattern without opening anything.
type HistoryCard struct {
	At             time.Time `json:"at"`
	Candidate      string    `json:"candidate"`
	Recommendation string    `json:"recommendation"`
	Lane           string    `json:"lane,omitempty"`
	// Reason is one short influential reason. One, because a card that grew
	// into a paragraph would stop being a card.
	Reason  string `json:"reason"`
	Outcome string `json:"outcome"`
}

// Store is one Application and Environment's journal.
type Store struct {
	// Dir is the environment directory. Everything lives under it.
	Dir string
}

func (s Store) recordsDir() string  { return filepath.Join(s.Dir, "releases") }
func (s Store) activePath() string  { return filepath.Join(s.Dir, "active") }
func (s Store) latestPath() string  { return filepath.Join(s.Dir, "latest") }
func (s Store) historyPath() string { return filepath.Join(s.Dir, "history.jsonl") }

func (s Store) recordPath(id string) string {
	return filepath.Join(s.recordsDir(), id+".json")
}

// Start opens a new release attempt.
//
// There is at most one active release per Application and Environment, and
// this is where that holds. A second one would mean a person could approve one
// recommendation and run the other, and no command takes an identifier that
// would let them tell which.
func (s Store) Start(record Record) (Record, error) {
	if active, found, err := s.Active(); err != nil {
		return Record{}, err
	} else if found && !active.State.Terminal() {
		return Record{}, release.Invalid("release_already_active", "release",
			fmt.Sprintf("a release of %s to %s is already in progress", active.Application, active.Environment),
			"Finish or stop it first.")
	}
	if record.ID == "" {
		return Record{}, release.Internal("missing_release_id", "a release record needs an identifier")
	}
	if record.Attempt < 1 {
		record.Attempt = 1
	}
	if err := s.Save(record); err != nil {
		return Record{}, err
	}
	if err := writeFileAtomic(s.activePath(), []byte(record.ID+"\n")); err != nil {
		return Record{}, err
	}
	return record, nil
}

// Retry opens a new attempt from a finished one.
//
// A new record, with a new identifier. Reopening the old one would mean the
// evidence, the recommendation and the approval that were true for the first
// attempt now describe the second, and none of them do.
func (s Store) Retry(previous Record, now time.Time) (Record, error) {
	next := Record{
		ID:          NewID(previous.Application, previous.Environment, previous.Attempt+1, now),
		Application: previous.Application,
		Environment: previous.Environment,
		Candidate:   previous.Candidate,
		State:       StateAssessing,
		Attempt:     previous.Attempt + 1,
		Previous:    previous.ID,
		Started:     now,
		Events: []Event{{
			At: now, Kind: "retried", By: ActorUser,
			Detail: fmt.Sprintf("a new attempt after %s", previous.Outcome),
		}},
	}
	return s.Start(next)
}

// NewID is a readable, sortable identifier. It is internal: no command takes
// one, because the Application and Environment already resolve the release.
func NewID(application, environment string, attempt int, now time.Time) string {
	return fmt.Sprintf("%s-%s-%s-%d", application, environment, now.UTC().Format("20060102T150405Z"), attempt)
}

// Save writes a record atomically.
func (s Store) Save(record Record) error {
	if err := os.MkdirAll(s.recordsDir(), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", s.recordsDir(), err)
	}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.recordPath(record.ID), append(raw, '\n'))
}

// Load reads one record by identifier.
func (s Store) Load(id string) (Record, bool, error) {
	raw, err := os.ReadFile(s.recordPath(id))
	if os.IsNotExist(err) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	var record Record
	if err := json.Unmarshal(raw, &record); err != nil {
		return Record{}, false, release.Invalid("unreadable_release_record", "release",
			fmt.Sprintf("could not read release record %s: %v", id, err),
			"Ask me to look at this application again.")
	}
	return record, true, nil
}

// Active is the release this Application and Environment is currently in the
// middle of, if any.
func (s Store) Active() (Record, bool, error) {
	raw, err := os.ReadFile(s.activePath())
	if os.IsNotExist(err) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	return s.Load(strings.TrimSpace(string(raw)))
}

// Latest is the newest terminal record. It keeps detailed proof reachable
// after Finish releases the active slot.
func (s Store) Latest() (Record, bool, error) {
	raw, err := os.ReadFile(s.latestPath())
	if os.IsNotExist(err) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	return s.Load(strings.TrimSpace(string(raw)))
}

// Append adds an event and saves. Events are only ever appended.
func (s Store) Append(record Record, event Event) (Record, error) {
	record.Events = append(record.Events, event)
	if event.Weight > 0 {
		record.Weight = event.Weight
	}
	return record, s.Save(record)
}

// Finish closes a release: it writes the terminal state, appends the compact
// card, and releases the active slot.
//
// The card is written from the record, so the compact history and the detailed
// proof cannot disagree about what happened - there is only one place the
// answer comes from.
func (s Store) Finish(record Record, state State, outcome, reason string, now time.Time) (Record, error) {
	if !state.Terminal() {
		return record, release.Internal("not_a_terminal_state",
			fmt.Sprintf("%q does not end a release", state))
	}
	record.State = state
	record.Outcome = outcome
	if reason != "" {
		record.Reason = reason
	}
	record.Ended = now
	if err := s.Save(record); err != nil {
		return record, err
	}
	if err := s.appendCard(record.Card()); err != nil {
		return record, err
	}
	if err := writeFileAtomic(s.latestPath(), []byte(record.ID+"\n")); err != nil {
		return record, err
	}
	if err := os.Remove(s.activePath()); err != nil && !os.IsNotExist(err) {
		return record, err
	}
	return record, nil
}

// Card is the compact form of this record. One function, so the two views of a
// release are derived rather than maintained.
func (r Record) Card() HistoryCard {
	recommendation := "wait"
	if r.Lane != "" {
		recommendation = "proceed"
	}
	return HistoryCard{
		At:             r.Ended,
		Candidate:      r.Candidate,
		Recommendation: recommendation,
		Lane:           r.Lane,
		Reason:         r.Reason,
		Outcome:        r.Outcome,
	}
}

func (s Store) appendCard(card HistoryCard) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(card)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(s.historyPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(raw, '\n'))
	return err
}

// History returns the newest cards, newest first, at most limit of them.
//
// Bounded because the normal evidence view is meant to be cheap. Older history
// is still on disk and still readable; it loads when somebody has a concrete
// question about it, not by default.
func (s Store) History(limit int) ([]HistoryCard, error) {
	file, err := os.Open(s.historyPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var cards []HistoryCard
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var card HistoryCard
		if err := json.Unmarshal([]byte(line), &card); err != nil {
			continue
		}
		cards = append(cards, card)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// Newest first.
	for i, j := 0, len(cards)-1; i < j; i, j = i+1, j-1 {
		cards[i], cards[j] = cards[j], cards[i]
	}
	if limit > 0 && len(cards) > limit {
		cards = cards[:limit]
	}
	return cards, nil
}

func writeFileAtomic(path string, content []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".journal.*")
	if err != nil {
		return err
	}
	name := temp.Name()
	if _, err := temp.Write(content); err != nil {
		temp.Close()
		os.Remove(name)
		return err
	}
	if err := temp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}
