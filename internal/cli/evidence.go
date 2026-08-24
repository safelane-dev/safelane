package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/AndrewMaged814/safelane/internal/config"
	"github.com/AndrewMaged814/safelane/internal/delta"
	"github.com/AndrewMaged814/safelane/internal/release"
)

var diffHandleID = regexp.MustCompile(`^diff:sha256:([0-9a-f]{64})$`)

// DiffSource reloads a source range by its immutable endpoints. Evidence
// verifies the returned bytes against the frozen content-addressed handle.
type DiffSource interface {
	RawDiff(ctx context.Context, repository, base, head string) ([]byte, error)
}

type diffLocator struct {
	Handle     delta.Handle `json:"handle"`
	Repository string       `json:"repository"`
	Base       string       `json:"base"`
	Head       string       `json:"head"`
}

type EvidenceOptions struct {
	Root        string
	Home        string
	Application string
	Environment string
	HandleID    string
	Origin      func(string) (string, error)
	Source      DiffSource
}

// Evidence resolves one heavy evidence handle without carrying raw source in
// release state or the agent's ordinary context.
func Evidence(ctx context.Context, opts EvidenceOptions, stdout, stderr io.Writer) int {
	application, err := applicationFrom(opts.Root, opts.Home, opts.Application, opts.Origin)
	if err != nil {
		return writeResultError(stderr, "evidence", err)
	}
	cfg, err := config.Load(config.ForApp(opts.Home, application).File)
	if err != nil {
		return writeResultError(stderr, "evidence", err)
	}
	if _, ok := cfg.Environment(opts.Environment); !ok {
		return writeResultError(stderr, "evidence", unknownEnvironment(application, opts.Environment, cfg))
	}
	dir := config.ForApp(opts.Home, application).ForEnvironment(opts.Environment).Dir
	locator, err := loadDiffLocator(dir, opts.HandleID)
	if err != nil {
		return writeResultError(stderr, "evidence", err)
	}
	content, err := opts.Source.RawDiff(ctx, locator.Repository, locator.Base, locator.Head)
	if err != nil {
		return writeResultError(stderr, "evidence", sourceEvidenceUnavailable("source diff", err))
	}
	if err := locator.Handle.Verify(content); err != nil {
		return writeResultError(stderr, "evidence", release.UnknownEvidenceError("source_diff_changed", "diff",
			err.Error(), "Inspect the release again; the retrieved evidence did not match the frozen snapshot."))
	}
	if _, err := stdout.Write(content); err != nil {
		return writeResultError(stderr, "evidence", err)
	}
	return ExitOK
}

func diffLocatorPath(environmentDir, handleID string) (string, error) {
	match := diffHandleID.FindStringSubmatch(handleID)
	if match == nil {
		return "", release.Invalid("invalid_evidence_handle", "handle",
			fmt.Sprintf("%q is not a source diff handle", handleID), "Use a diff handle from the frozen Release Delta.")
	}
	return filepath.Join(environmentDir, "evidence", match[1]+".json"), nil
}

func saveDiffLocator(environmentDir string, locator diffLocator) error {
	path, err := diffLocatorPath(environmentDir, locator.Handle.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(locator, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".diff-locator.*.json")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	removeTemporary := func() { _ = os.Remove(temporaryName) }
	if _, err := temporary.Write(append(raw, '\n')); err != nil {
		_ = temporary.Close()
		removeTemporary()
		return err
	}
	if err := temporary.Close(); err != nil {
		removeTemporary()
		return err
	}
	if err := os.Chmod(temporaryName, 0o600); err != nil {
		removeTemporary()
		return err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		removeTemporary()
		return err
	}
	return nil
}

func loadDiffLocator(environmentDir, handleID string) (diffLocator, error) {
	path, err := diffLocatorPath(environmentDir, handleID)
	if err != nil {
		return diffLocator{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return diffLocator{}, release.UnknownEvidenceError("evidence_handle_unavailable", "handle",
			fmt.Sprintf("SafeLane cannot resolve %s: %v", handleID, err), "Inspect the release again to freeze this evidence.")
	}
	var locator diffLocator
	if err := json.Unmarshal(raw, &locator); err != nil || locator.Handle.ID != handleID {
		return diffLocator{}, release.UnknownEvidenceError("invalid_evidence_locator", "handle",
			fmt.Sprintf("SafeLane cannot trust the saved locator for %s", handleID), "Inspect the release again to replace it.")
	}
	return locator, nil
}
