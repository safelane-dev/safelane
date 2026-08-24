package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/AndrewMaged814/safelane/internal/config"
	"github.com/AndrewMaged814/safelane/internal/release"
	githubverify "github.com/AndrewMaged814/safelane/internal/verify/github"
)

const confirmedBuildFile = "confirmed-build.json"

type confirmedBuild struct {
	Candidate string `json:"candidate"`
	Digest    string `json:"digest"`
	RunID     int64  `json:"run_id"`
	RunName   string `json:"run_name"`
}

type ConfirmBuildOptions struct {
	Inspect InspectOptions
	RunID   string
}

// ConfirmBuild records which one of the successful exact-revision workflows
// produced the selected artifact. It is release-scoped evidence, never config.
func ConfirmBuild(ctx context.Context, opts ConfirmBuildOptions, stdout, stderr io.Writer) int {
	runID, err := strconv.ParseInt(strings.TrimSpace(opts.RunID), 10, 64)
	if err != nil || runID <= 0 {
		return writeResultError(stderr, "confirm-build", release.Invalid("invalid_workflow_run", "workflow",
			"the selected workflow run is not valid", "Choose one of the successful runs SafeLane listed."))
	}
	frozen, _, err := FreezeDelta(ctx, opts.Inspect)
	if err != nil {
		return writeResultError(stderr, "confirm-build", err)
	}
	cfg, err := configFor(opts.Inspect, frozen.Application())
	if err != nil {
		return writeResultError(stderr, "confirm-build", err)
	}
	checks, err := opts.Inspect.Source.Checks(ctx, cfg.Application.Repository, frozen.Candidate().Revision)
	if err != nil {
		return writeResultError(stderr, "confirm-build", err)
	}
	var selected githubverify.WorkflowRun
	for _, run := range checks.SuccessfulWorkflows() {
		if run.ID == runID && strings.EqualFold(run.HeadSHA, frozen.Candidate().Revision) {
			selected = run
			break
		}
	}
	if selected.ID == 0 {
		return writeResultError(stderr, "confirm-build", release.Invalid("workflow_did_not_build_candidate", "workflow",
			fmt.Sprintf("workflow run %d is not a successful run for this exact candidate", runID),
			"Choose one of the successful runs SafeLane listed."))
	}
	confirmation := confirmedBuild{
		Candidate: frozen.Candidate().Revision, Digest: frozen.Candidate().Digest,
		RunID: selected.ID, RunName: selected.Name,
	}
	dir := config.ForApp(opts.Inspect.Home, frozen.Application()).ForEnvironment(frozen.Environment()).Dir
	if err := saveConfirmedBuild(filepath.Join(dir, confirmedBuildFile), confirmation); err != nil {
		return writeResultError(stderr, "confirm-build", err)
	}
	if RenderingFor(stdout, opts.Inspect.ForceJSON) == RenderJSON {
		return writeControlJSON(stdout, stderr, "confirm-build", map[string]any{
			"confirmed": true, "workflow": selected.Name, "candidate": frozen.Candidate().Revision,
		})
	}
	fmt.Fprintf(stdout, "Confirmed that workflow %q produced the container for this release.\n", selected.Name)
	return ExitOK
}

func loadConfirmedBuild(path, candidate, digest string) int64 {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var confirmation confirmedBuild
	if json.Unmarshal(raw, &confirmation) != nil {
		return 0
	}
	if !strings.EqualFold(confirmation.Candidate, candidate) || confirmation.Digest != digest {
		return 0
	}
	return confirmation.RunID
}

func saveConfirmedBuild(path string, confirmation confirmedBuild) error {
	raw, err := json.MarshalIndent(confirmation, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary := path + ".new"
	if err := os.WriteFile(temporary, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
