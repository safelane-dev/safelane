package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/AndrewMaged814/safelane/internal/config"
	"github.com/AndrewMaged814/safelane/internal/journal"
	"github.com/AndrewMaged814/safelane/internal/release"
)

// ControlOptions are what `status`, `hold`, `continue`, `stop` and `proof`
// need.
//
// None of them takes a release identifier. There is one active release per
// Application and Environment, so the pair resolves it - and the previous
// design's `--release <internal-id>` contradicted the rule that identifiers
// stay internal. A person who has to look up an identifier to stop a rollout
// is a person who will not stop it in time.
type ControlOptions struct {
	Root        string
	Home        string
	Environment string
	App         string
	ForceJSON   bool
	// Reason is the positional text on hold, continue and stop.
	Reason string
	// Details is `--details` on proof.
	Details bool
	// Observe reads what the cluster currently says, so status can reconcile.
	// Nil means the stored record is reported as-is.
	Observe func(ctx context.Context, environment config.Environment) (journal.Observed, error)
	// Control asks Argo to hold, continue or stop. Nil means the record is
	// updated without touching the cluster.
	Control func(ctx context.Context, action string, environment config.Environment) error
	Now     func() time.Time
	// Origin is the injected Git-origin read.
	Origin func(root string) (string, error)
}

// observe and control fall back to the real cluster. Nil means production;
// a test substitutes either without production having to remember to pass one.
func (o ControlOptions) observe(environment config.Environment) func(context.Context, config.Environment) (journal.Observed, error) {
	if o.Observe != nil {
		return o.Observe
	}
	if o.Home == "" {
		return nil
	}
	return o.cluster(environment).Observe
}

func (o ControlOptions) control(environment config.Environment) func(context.Context, string, config.Environment) error {
	if o.Control != nil {
		return o.Control
	}
	return o.cluster(environment).Control
}

func (o ControlOptions) cluster(environment config.Environment) Cluster {
	application, _ := applicationFrom(o.Root, o.Home, o.App, o.Origin)
	return Cluster{Home: o.Home, Application: application, Environment: environment}
}

func (o ControlOptions) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now().UTC()
}

// resolve finds the active release from the Application and Environment.
func (o ControlOptions) resolve() (journal.Store, config.Environment, journal.Record, error) {
	application, err := applicationFrom(o.Root, o.Home, o.App, o.Origin)
	if err != nil {
		return journal.Store{}, config.Environment{}, journal.Record{}, err
	}
	cfg, err := config.Load(config.ForApp(o.Home, application).File)
	if err != nil {
		return journal.Store{}, config.Environment{}, journal.Record{}, err
	}
	environment, ok := cfg.Environment(o.Environment)
	if !ok {
		return journal.Store{}, config.Environment{}, journal.Record{},
			unknownEnvironment(application, o.Environment, cfg)
	}
	store := journal.Store{Dir: config.ForApp(o.Home, application).ForEnvironment(environment.Name).Dir}

	record, found, err := store.Active()
	if err != nil {
		return store, environment, journal.Record{}, err
	}
	if !found {
		return store, environment, journal.Record{}, release.Invalid("no_active_release", "release",
			fmt.Sprintf("there is no release of %s to %s in progress", application, environment.Name),
			fmt.Sprintf("Ask me to look at releasing %s to %s.", application, environment.Name))
	}
	return store, environment, record, nil
}

// Status says where the release is and what it is waiting for.
//
// When the stored record and the observed Rollout disagree, the Rollout wins.
// SafeLane reconciles, says so, and never asks a person to decide which to
// believe - that would be asking them to do SafeLane's job.
func Status(ctx context.Context, opts ControlOptions, stdout, stderr io.Writer) int {
	store, environment, record, err := opts.resolve()
	if err != nil {
		return writeResultError(stderr, "status", err)
	}

	correction := ""
	if observe := opts.observe(environment); observe != nil {
		observed, observeErr := observe(ctx, environment)
		if observeErr != nil {
			return writeResultError(stderr, "status", observeErr)
		}
		var changed bool
		record, correction, changed = journal.Reconcile(record, observed, opts.now())
		if changed {
			if err := store.Save(record); err != nil {
				return writeResultError(stderr, "status", err)
			}
		}
	}

	if RenderingFor(stdout, opts.ForceJSON) == RenderJSON {
		return writeControlJSON(stdout, stderr, "status", map[string]any{
			"state":       record.State,
			"waiting_for": record.Status().WaitingFor(),
			"line":        record.Status().Line(),
			"weight":      record.Weight,
			"correction":  correction,
		})
	}
	if correction != "" {
		fmt.Fprintf(stdout, "%s\n\n", correction)
	}
	fmt.Fprintln(stdout, record.Status().Line())
	return ExitOK
}

// Hold pauses without widening exposure. It does not roll anything back: a
// hold is "stay where you are", and a person who wanted the stable version
// back would have said stop.
func Hold(ctx context.Context, opts ControlOptions, stdout, stderr io.Writer) int {
	return control(ctx, opts, stdout, stderr, "hold", journal.StatePaused,
		"held at your request", "")
}

// Continue resumes a held release. It requires explicit intent, and the reason
// goes into the record next to the hold it answers.
func Continue(ctx context.Context, opts ControlOptions, stdout, stderr io.Writer) int {
	return control(ctx, opts, stdout, stderr, "continue", journal.StateMonitoring,
		"continued at your request", "")
}

// Stop asks Argo to abort and restore the stable version, and records why.
func Stop(ctx context.Context, opts ControlOptions, stdout, stderr io.Writer) int {
	return control(ctx, opts, stdout, stderr, "stop", journal.StateStopped,
		"stopped at your request", "stopped at your request; stable version restored")
}

func control(ctx context.Context, opts ControlOptions, stdout, stderr io.Writer,
	action string, next journal.State, detail, outcome string) int {

	if err := journal.ControlReason(action, opts.Reason); err != nil {
		return writeResultError(stderr, action, err)
	}
	store, environment, record, err := opts.resolve()
	if err != nil {
		return writeResultError(stderr, action, err)
	}
	if record.State.Terminal() {
		return writeResultError(stderr, action, release.Invalid("release_is_over", "release",
			"this release has already finished",
			"Ask me to look at releasing again if you want a new one."))
	}

	if err := opts.control(environment)(ctx, action, environment); err != nil {
		return writeResultError(stderr, action, err)
	}

	now := opts.now()
	record.Reason = opts.Reason
	record, err = store.Append(record, journal.Event{
		At: now, Kind: action, By: journal.ActorUser,
		Detail: detail + ": " + opts.Reason, Weight: record.Weight,
	})
	if err != nil {
		return writeResultError(stderr, action, err)
	}

	if next.Terminal() {
		record, err = store.Finish(record, next, outcome, opts.Reason, now)
	} else {
		record.State = next
		err = store.Save(record)
	}
	if err != nil {
		return writeResultError(stderr, action, err)
	}

	if RenderingFor(stdout, opts.ForceJSON) == RenderJSON {
		return writeControlJSON(stdout, stderr, action, map[string]any{
			"state":  record.State,
			"line":   record.Status().Line(),
			"reason": opts.Reason,
		})
	}
	fmt.Fprintln(stdout, record.Status().Line())
	return ExitOK
}

// Proof shows what happened. Compact by default; `--details` opens the whole
// record.
//
// `--details` stays opt-in because loading full proof by default would spend
// an agent's context on records nobody asked for.
func Proof(ctx context.Context, opts ControlOptions, stdout, stderr io.Writer) int {
	application, err := applicationFrom(opts.Root, opts.Home, opts.App, opts.Origin)
	if err != nil {
		return writeResultError(stderr, "proof", err)
	}
	cfg, err := config.Load(config.ForApp(opts.Home, application).File)
	if err != nil {
		return writeResultError(stderr, "proof", err)
	}
	environment, ok := cfg.Environment(opts.Environment)
	if !ok {
		return writeResultError(stderr, "proof", unknownEnvironment(application, opts.Environment, cfg))
	}
	store := journal.Store{Dir: config.ForApp(opts.Home, application).ForEnvironment(environment.Name).Dir}

	cards, err := store.History(journalHistoryLimit)
	if err != nil {
		return writeResultError(stderr, "proof", err)
	}

	if opts.Details {
		record, found, loadErr := store.Active()
		if loadErr != nil {
			return writeResultError(stderr, "proof", loadErr)
		}
		if !found {
			return writeResultError(stderr, "proof", release.Invalid("no_release_to_detail", "release",
				fmt.Sprintf("there is no release of %s to %s in progress", application, environment.Name),
				"Ask for the history instead, or start a release."))
		}
		return writeControlJSON(stdout, stderr, "proof", map[string]any{
			"record":  record,
			"history": cards,
		})
	}

	if RenderingFor(stdout, opts.ForceJSON) == RenderJSON {
		return writeControlJSON(stdout, stderr, "proof", map[string]any{"history": cards})
	}
	if len(cards) == 0 {
		fmt.Fprintf(stdout, "No previous release of %s to %s.\n", application, environment.Name)
		return ExitOK
	}
	for _, card := range cards {
		fmt.Fprintf(stdout, "%s  %s  %s", card.At.UTC().Format("2006-01-02 15:04"), shortSHA(card.Candidate), card.Outcome)
		if card.Lane != "" {
			fmt.Fprintf(stdout, " (%s lane)", card.Lane)
		}
		if card.Reason != "" {
			fmt.Fprintf(stdout, "  %s", card.Reason)
		}
		fmt.Fprintln(stdout)
	}
	return ExitOK
}

// journalHistoryLimit is the same ten the evidence view uses. One number, so
// the two views of history cannot drift apart.
const journalHistoryLimit = 10

func writeControlJSON(stdout, stderr io.Writer, command string, payload map[string]any) int {
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(payload); err != nil {
		return writeResultError(stderr, command, err)
	}
	return ExitOK
}

// shortSHA abbreviates a commit for a history line. Proof keeps the exact
// revision; a list a person scans does not need forty characters of it.
func shortSHA(sha string) string {
	if len(sha) <= 7 {
		return sha
	}
	return sha[:7]
}
