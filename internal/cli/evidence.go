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
	"github.com/AndrewMaged814/safelane/internal/privatefile"
	"github.com/AndrewMaged814/safelane/internal/release"
)

var evidenceHandleID = regexp.MustCompile(`^analysis:sha256:([0-9a-f]{64})$`)

// evidenceLocator stores only content that crossed an explicit typed,
// secret-safe boundary. SafeLane intentionally does not expose arbitrary raw
// source for the POC; changed paths and commit summaries stay in ChangesView.
type evidenceLocator struct {
	Handle  delta.Handle `json:"handle"`
	Content []byte       `json:"content"`
}

type EvidenceOptions struct {
	Root        string
	Home        string
	Application string
	Environment string
	HandleID    string
	Origin      func(string) (string, error)
}

// Evidence resolves one detailed, typed evidence handle without carrying its
// body in release state or the agent's ordinary context.
func Evidence(_ context.Context, opts EvidenceOptions, stdout, stderr io.Writer) int {
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
	locator, err := loadEvidenceLocator(dir, opts.HandleID)
	if err != nil {
		return writeResultError(stderr, "evidence", err)
	}
	if err := locator.Handle.Verify(locator.Content); err != nil {
		return writeResultError(stderr, "evidence", err)
	}
	if _, err := stdout.Write(locator.Content); err != nil {
		return writeResultError(stderr, "evidence", err)
	}
	return ExitOK
}

func evidenceLocatorPath(environmentDir, handleID string) (string, error) {
	match := evidenceHandleID.FindStringSubmatch(handleID)
	if match == nil {
		return "", release.Invalid("invalid_evidence_handle", "handle",
			fmt.Sprintf("%q is not a supported evidence handle", handleID),
			"Use an AnalysisTemplate handle from the frozen Release Delta.")
	}
	return filepath.Join(environmentDir, "evidence", match[1]+".json"), nil
}

func saveEvidenceContent(environmentDir string, handle delta.Handle, content []byte) error {
	path, err := evidenceLocatorPath(environmentDir, handle.ID)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(evidenceLocator{Handle: handle, Content: content}, "", "  ")
	if err != nil {
		return err
	}
	return privatefile.WriteAtomic(path, append(raw, '\n'))
}

func loadEvidenceLocator(environmentDir, handleID string) (evidenceLocator, error) {
	path, err := evidenceLocatorPath(environmentDir, handleID)
	if err != nil {
		return evidenceLocator{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return evidenceLocator{}, release.UnknownEvidenceError("evidence_handle_unavailable", "handle",
			fmt.Sprintf("SafeLane cannot resolve %s: %v", handleID, err), "Inspect the release again to freeze this evidence.")
	}
	var locator evidenceLocator
	if err := json.Unmarshal(raw, &locator); err != nil || locator.Handle.ID != handleID {
		return evidenceLocator{}, release.UnknownEvidenceError("invalid_evidence_locator", "handle",
			fmt.Sprintf("SafeLane cannot trust the saved locator for %s", handleID), "Inspect the release again to replace it.")
	}
	return locator, nil
}
