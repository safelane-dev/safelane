package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/AndrewMaged814/safelane/internal/config"
	"github.com/AndrewMaged814/safelane/internal/discovery"
	"github.com/AndrewMaged814/safelane/internal/release"
)

// The command surface rules every SafeLane command follows. They live here,
// once, because a convention each command re-decides is not a convention.
//
//  1. No command prefix and no output flag. SafeLane is a release tool, so the
//     noun is implied; output is JSON when stdout is not a terminal and
//     readable text when it is. The agent path carries no flag at all, and a
//     person typing the same command gets something legible. `--json` exists
//     only to force machine output at a terminal.
//  2. The Environment is the positional argument, because it is the one thing
//     the user actually says.
//  3. The Application is inferred from the checkout's Git origin. `--app`
//     exists only to disambiguate, and its absence is not an error when
//     exactly one Application matches.

// Rendering is how one command's output should be written.
type Rendering int

const (
	// RenderText is for a person reading a terminal.
	RenderText Rendering = iota
	// RenderJSON is for a program reading a pipe.
	RenderJSON
)

// TerminalWriter is an output that knows whether it is a terminal.
//
// os.Stdout does not implement this - it is answered by its file mode below.
// The interface exists so both branches of rule 1 can be exercised without a
// real tty, which is the only way a golden test of the readable form can be
// written at all.
type TerminalWriter interface {
	IsTerminal() bool
}

// RenderingFor picks machine or readable output from where the output is going,
// so neither caller has to say.
//
// A pipe, a file, and a test buffer are all "not a terminal", which is the
// right answer for all three: something is reading this, and something that
// reads wants JSON.
func RenderingFor(w io.Writer, forceJSON bool) Rendering {
	if forceJSON {
		return RenderJSON
	}
	if known, ok := w.(TerminalWriter); ok {
		if known.IsTerminal() {
			return RenderText
		}
		return RenderJSON
	}
	file, ok := w.(*os.File)
	if !ok {
		return RenderJSON
	}
	info, err := file.Stat()
	if err != nil {
		return RenderJSON
	}
	if info.Mode()&os.ModeCharDevice != 0 {
		return RenderText
	}
	return RenderJSON
}

// ApplicationFor answers "which Application is this?" the way rule 3 says:
// from the repository the caller is standing in, not from something they typed.
func ApplicationFor(root, home, explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit), nil
	}
	repository, err := discovery.GitHubOrigin(root)
	if err != nil {
		return "", release.Invalid("no_github_origin", "application",
			"this directory has no GitHub origin, so SafeLane cannot tell which application you mean",
			"Run this from the application's repository, or name it with --app.")
	}
	return ResolveApplication(home, repository, explicit)
}

// ResolveApplication matches a repository against the registered Applications.
//
// An explicit name wins, one match is the answer, no match says register, and
// more than one match is the only case where a person is asked to be specific.
// That last case is why `--app` exists, and the only reason it does.
func ResolveApplication(home, repository, explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit), nil
	}

	names, err := config.Apps(home)
	if err != nil {
		return "", err
	}
	var matches []string
	for _, name := range names {
		cfg, loadErr := config.Load(config.ForApp(home, name).File)
		if loadErr != nil {
			continue
		}
		if strings.EqualFold(cfg.Application.Repository, repository) {
			matches = append(matches, name)
		}
	}
	sort.Strings(matches)

	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", release.Invalid("unregistered_application", "application",
			fmt.Sprintf("no application is registered for %s", repository),
			"Register this application first.")
	default:
		return "", release.Invalid("ambiguous_application", "application",
			fmt.Sprintf("%s is registered as more than one application: %s", repository, strings.Join(matches, ", ")),
			"Say which one with --app.")
	}
}
