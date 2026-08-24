package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/AndrewMaged814/safelane/internal/assessment"
	"github.com/AndrewMaged814/safelane/internal/config"
	"github.com/AndrewMaged814/safelane/internal/release"
	"github.com/AndrewMaged814/safelane/internal/releasepatch"
)

// ApproveOptions records the user's answer to the final approval question.
// The command is an agent adapter: it does not assess, apply, or select a
// release. Application and Environment still resolve the one pending
// recommendation.
type ApproveOptions struct {
	Root        string
	Home        string
	Environment string
	App         string
	Answer      string
	ForceJSON   bool
	Origin      func(root string) (string, error)
	Now         func() time.Time
}

func (o ApproveOptions) now() time.Time {
	if o.Now != nil {
		return o.Now().UTC()
	}
	return time.Now().UTC()
}

// Approve binds one explicit answer to the exact pending recommendation. It
// deliberately does not run the release. Keeping approval and mutation as two
// separate tool calls makes the agent trace and stored proof say where the
// user's decision occurred.
func Approve(_ context.Context, opts ApproveOptions, stdout, stderr io.Writer) int {
	if !releasepatch.IsApproval(opts.Answer, true) {
		return writeResultError(stderr, "approve", release.Invalid("approval_not_given", "approval",
			fmt.Sprintf("%q is not an approval of this release", strings.TrimSpace(opts.Answer)),
			"Ask the user the final rollout question and pass their answer exactly."))
	}

	application, err := applicationFrom(opts.Root, opts.Home, opts.App, opts.Origin)
	if err != nil {
		return writeResultError(stderr, "approve", err)
	}
	cfg, err := config.Load(config.ForApp(opts.Home, application).File)
	if err != nil {
		return writeResultError(stderr, "approve", err)
	}
	environment, ok := cfg.Environment(opts.Environment)
	if !ok {
		return writeResultError(stderr, "approve", unknownEnvironment(application, opts.Environment, cfg))
	}
	dir := config.ForApp(opts.Home, application).ForEnvironment(environment.Name).Dir
	pending, found, err := releasepatch.LoadPending(dir)
	if err != nil {
		return writeResultError(stderr, "approve", err)
	}
	if !found {
		return writeResultError(stderr, "approve", releasepatch.NothingAwaitingApproval(application, environment.Name))
	}
	if pending.Action != string(assessment.ActionProceed) {
		return writeResultError(stderr, "approve", releasepatch.WaitingCannotRun(application, environment.Name))
	}
	if pending.Approval != nil {
		return writeResultError(stderr, "approve", release.Invalid("approval_already_recorded", "approval",
			"this recommendation already has an explicit approval",
			"Run this release, or ask me to reassess if something changed."))
	}

	approval, err := releasepatch.Grant(application, environment.Name, pending.Snapshot,
		pending.Revision, pending.Patch, pending.Analysis, true, opts.now())
	if err != nil {
		return writeResultError(stderr, "approve", err)
	}
	pending.Approval = &approval
	if err := releasepatch.SavePending(dir, pending); err != nil {
		return writeResultError(stderr, "approve", err)
	}

	if RenderingFor(stdout, opts.ForceJSON) == RenderJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(map[string]any{
			"approved": true, "application": application, "environment": environment.Name,
		}); err != nil {
			return writeResultError(stderr, "approve", err)
		}
		return ExitOK
	}
	fmt.Fprintln(stdout, "Approval recorded for this release.")
	return ExitOK
}
