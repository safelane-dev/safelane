package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/AndrewMaged814/safelane/internal/discovery"
	"github.com/AndrewMaged814/safelane/internal/release"
)

// RegisterOptions are everything `safelane register` needs.
type RegisterOptions struct {
	Root string
	Home string
	// SelectionPath is a file, or "-" for stdin.
	SelectionPath string
	// App is `--app`, used only when the selection leaves the application
	// blank and the checkout is registered as more than one.
	App       string
	ForceJSON bool
	Service   discovery.Service
	Stdin     io.Reader
}

// Register confirms a selection against a fresh read of the cluster, writes the
// configuration, and prints the readiness summary.
func Register(ctx context.Context, opts RegisterOptions, stdout, stderr io.Writer) int {
	selection, err := readSelection(opts)
	if err != nil {
		return writeResultError(stderr, "register", err)
	}
	if strings.TrimSpace(selection.Application) == "" {
		name, resolveErr := ApplicationFor(opts.Root, opts.Home, opts.App)
		if resolveErr != nil {
			return writeResultError(stderr, "register", resolveErr)
		}
		selection.Application = name
	}

	registered, err := opts.Service.Register(ctx, opts.Root, opts.Home, selection)
	if err != nil {
		return writeResultError(stderr, "register", err)
	}

	if RenderingFor(stdout, opts.ForceJSON) == RenderJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(newRegistrationResult(registered)); err != nil {
			return writeResultError(stderr, "register", err)
		}
		return ExitOK
	}
	fmt.Fprint(stdout, RenderRegistration(registered))
	return ExitOK
}

// registrationResult is the machine form. It carries the file that was
// written, so an agent can show the same preview a person saw without reading
// the disk.
type registrationResult struct {
	Application string   `json:"application"`
	Environment string   `json:"environment"`
	Path        string   `json:"path"`
	Changed     bool     `json:"changed"`
	File        string   `json:"file"`
	Rollout     string   `json:"rollout"`
	Container   string   `json:"container"`
	Namespace   string   `json:"namespace"`
	Analysis    string   `json:"analysis,omitempty"`
	Provider    string   `json:"analysis_provider,omitempty"`
	Artifact    []string `json:"artifact_warnings,omitempty"`
}

func newRegistrationResult(r discovery.Registration) registrationResult {
	analysis, provider := r.Analysis()
	result := registrationResult{
		Application: r.Selection.Application,
		Environment: r.Selection.Environment,
		Path:        r.Path,
		Changed:     r.Changed,
		File:        string(r.File),
		Rollout:     r.Target.Rollout,
		Container:   r.Selection.Container,
		Namespace:   r.Target.Namespace,
		Analysis:    analysis,
		Provider:    provider,
	}
	for _, reason := range r.Target.Artifact.Reasons {
		result.Artifact = append(result.Artifact, reason.Explanation)
	}
	return result
}

// RenderRegistration writes Appendix A1.
//
// The wording, order and labels are the contract. It says what SafeLane will
// release, what it will watch, what it will change, what it will never change,
// and the one sentence the user can say next.
func RenderRegistration(r discovery.Registration) string {
	analysis, provider := r.Analysis()
	watching := fmt.Sprintf("background analysis %q", analysis)
	if provider != "" {
		watching += ", checked by " + provider
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Registered %s in %s.\n\n", r.Selection.Application, r.Selection.Environment)
	a1Field(&b, "Releasing", fmt.Sprintf("container %q in Rollout %q (namespace %s)",
		r.Selection.Container, r.Target.Rollout, r.Target.Namespace))
	a1Field(&b, "Watching", watching)
	a1Field(&b, "Will change", "the container image, and the canary steps")
	a1Field(&b, "Will not change", "probes, resources, replicas, environment, secrets, ports, Services,")
	a1Field(&b, "", "the traffic router, or your health analysis")
	fmt.Fprintf(&b, "\nSay \"Deploy %s to %s\" when you are ready.\n",
		r.Selection.Application, r.Selection.Environment)

	// Artifact traceability is reported separately from Kubernetes
	// compatibility, because an application can pass one and fail the other
	// and "incompatible" would hide which.
	if !r.Target.Artifact.Supported {
		b.WriteString("\nBefore the first release\n")
		for _, reason := range r.Target.Artifact.Reasons {
			fmt.Fprintf(&b, "%s\n", reason.Explanation)
		}
	}
	return b.String()
}

// a1LabelWidth is the A1 column: the longest label plus two spaces, so
// continuation lines line up under the values rather than under the labels.
const a1LabelWidth = len("Will not change") + 2

func a1Field(b *strings.Builder, label, value string) {
	fmt.Fprintf(b, "%-*s%s\n", a1LabelWidth, label, value)
}

func readSelection(opts RegisterOptions) (discovery.Selection, error) {
	var raw []byte
	var err error
	switch strings.TrimSpace(opts.SelectionPath) {
	case "":
		return discovery.Selection{}, release.Invalid("missing_selection", "selection",
			"no selection was given",
			"Pass the path to the confirmed selection, or - to read it from stdin.")
	case "-":
		if opts.Stdin == nil {
			return discovery.Selection{}, release.Invalid("missing_selection", "selection",
				"nothing was piped in",
				"Pass the path to the confirmed selection, or pipe it in.")
		}
		raw, err = io.ReadAll(opts.Stdin)
	default:
		raw, err = os.ReadFile(opts.SelectionPath)
	}
	if err != nil {
		return discovery.Selection{}, release.Invalid("unreadable_selection", "selection",
			fmt.Sprintf("could not read the selection: %v", err),
			"Check the path and try again.")
	}

	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var selection discovery.Selection
	if err := decoder.Decode(&selection); err != nil {
		return discovery.Selection{}, release.Malformed("invalid_selection", "selection",
			fmt.Sprintf("the selection is not readable: %v", err),
			"Send only the fields discovery asked about.")
	}
	return selection, nil
}
