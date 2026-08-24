package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/AndrewMaged814/safelane/internal/config"
	"github.com/AndrewMaged814/safelane/internal/delta"
	"github.com/AndrewMaged814/safelane/internal/privatefile"
	"github.com/AndrewMaged814/safelane/internal/release"
)

var evidenceHandleID = regexp.MustCompile(`^(diff|analysis):sha256:([0-9a-f]{64})$`)
var highEntropyLiteral = regexp.MustCompile(`[A-Za-z0-9+/=_-]{24,}`)

// DiffSource reloads a source range by its immutable endpoints. Evidence
// verifies the returned bytes against the frozen content-addressed handle.
type DiffSource interface {
	RawDiff(ctx context.Context, repository, base, head string) ([]byte, error)
}

type diffLocator struct {
	Handle     delta.Handle      `json:"handle"`
	Repository string            `json:"repository"`
	Base       string            `json:"base"`
	Head       string            `json:"head"`
	Excluded   map[string]string `json:"excluded,omitempty"`
	Content    []byte            `json:"content,omitempty"`
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
	if len(locator.Content) > 0 {
		if err := locator.Handle.Verify(locator.Content); err != nil {
			return writeResultError(stderr, "evidence", err)
		}
		if _, err := stdout.Write(locator.Content); err != nil {
			return writeResultError(stderr, "evidence", err)
		}
		return ExitOK
	}
	content, err := opts.Source.RawDiff(ctx, locator.Repository, locator.Base, locator.Head)
	if err != nil {
		return writeResultError(stderr, "evidence", sourceEvidenceUnavailable("source diff", err))
	}
	content = sanitizeDiff(content, locator.Excluded)
	if err := locator.Handle.Verify(content); err != nil {
		return writeResultError(stderr, "evidence", release.UnknownEvidenceError("source_diff_changed", "diff",
			err.Error(), "Inspect the release again; the retrieved evidence did not match the frozen snapshot."))
	}
	if _, err := stdout.Write(content); err != nil {
		return writeResultError(stderr, "evidence", err)
	}
	return ExitOK
}

func excludedDiffFiles(files []delta.File) map[string]string {
	excluded := map[string]string{}
	for _, file := range files {
		if file.SecretReference != "" {
			excluded[string(file.Path)] = file.SecretReference
		}
	}
	return excluded
}

func sanitizeDiff(content []byte, excluded map[string]string) []byte {
	var output []byte
	sections := bytes.SplitAfter(content, []byte("\n"))
	skipping := false
	for _, line := range sections {
		text := strings.TrimSuffix(string(line), "\n")
		if strings.HasPrefix(text, "diff --git ") {
			skipping = false
			for path, reference := range excluded {
				if strings.Contains(text, " b/"+path) || strings.Contains(text, ` "b/`+path+`"`) {
					output = append(output, line...)
					output = append(output, []byte("SafeLane omitted this file because it touches "+reference+".\n")...)
					skipping = true
					break
				}
			}
			if skipping {
				continue
			}
		}
		if !skipping {
			if sensitiveDiffLine(text) {
				prefix := text[:1]
				output = append(output, []byte(prefix+"[sensitive line omitted]\n")...)
			} else {
				output = append(output, line...)
			}
		}
	}
	return output
}

func diffExclusions(content []byte, references []string, excluded map[string]string) map[string]string {
	result := map[string]string{}
	for path, reference := range excluded {
		result[path] = reference
	}
	sections := bytes.Split(content, []byte("\ndiff --git "))
	for index, section := range sections {
		if index > 0 {
			section = append([]byte("diff --git "), section...)
		}
		header := string(bytes.SplitN(section, []byte("\n"), 2)[0])
		path := diffPath(header)
		if path == "" {
			continue
		}
		if sensitiveDiffPath(path) {
			result[path] = "sensitive source content"
			continue
		}
		lower := strings.ToLower(string(section))
		for _, reference := range references {
			name := reference
			if _, after, found := strings.Cut(reference, "/"); found {
				name = after
			}
			if name != "" && strings.Contains(lower, strings.ToLower(name)) {
				result[path] = reference
				break
			}
		}
	}
	return result
}

func sensitiveDiffPath(path string) bool {
	lower := strings.ToLower(path)
	for _, marker := range []string{".env", "secret", "credential", "private-key", "private_key", ".pem", ".p12"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func sensitiveDiffLine(line string) bool {
	if len(line) < 2 || (line[0] != '+' && line[0] != '-') || strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
		return false
	}
	lower := strings.ToLower(line)
	for _, marker := range []string{"password", "passwd", "secret", "token", "api_key", "apikey", "private_key", "credential", "authorization", "sk_live", "ghp_"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return highEntropyLiteral.MatchString(line)
}

func diffPath(header string) string {
	marker := strings.LastIndex(header, " b/")
	if marker < 0 {
		marker = strings.LastIndex(header, ` "b/`)
	}
	if marker < 0 {
		return ""
	}
	return strings.Trim(strings.TrimSpace(header[marker+3:]), `"`)
}

func diffLocatorPath(environmentDir, handleID string) (string, error) {
	match := evidenceHandleID.FindStringSubmatch(handleID)
	if match == nil {
		return "", release.Invalid("invalid_evidence_handle", "handle",
			fmt.Sprintf("%q is not a supported evidence handle", handleID), "Use a handle from the frozen Release Delta.")
	}
	return filepath.Join(environmentDir, "evidence", match[2]+".json"), nil
}

func saveEvidenceContent(environmentDir string, handle delta.Handle, content []byte) error {
	return saveDiffLocator(environmentDir, diffLocator{Handle: handle, Content: content})
}

func saveDiffLocator(environmentDir string, locator diffLocator) error {
	path, err := diffLocatorPath(environmentDir, locator.Handle.ID)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(locator, "", "  ")
	if err != nil {
		return err
	}
	return privatefile.WriteAtomic(path, append(raw, '\n'))
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
