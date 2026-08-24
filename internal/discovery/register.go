package discovery

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/AndrewMaged814/safelane/internal/config"
	"github.com/AndrewMaged814/safelane/internal/release"
)

// Selection is the whole of what a person answered. It carries explicit
// choices and the fingerprint of what they were looking at when they made
// them - nothing derived, nothing inferred, and no Kubernetes objects.
type Selection struct {
	Application string        `json:"application"`
	Environment string        `json:"environment"`
	Impact      config.Impact `json:"impact"`
	Context     string        `json:"context"`
	Namespace   string        `json:"namespace"`
	Rollout     string        `json:"rollout"`
	Container   string        `json:"container"`
	Fingerprint string        `json:"fingerprint"`
}

// Registration is the outcome: what was written, and what to say about it.
type Registration struct {
	Selection Selection
	// Target is the re-read of the cluster that registration confirmed
	// against, not the one discovery returned earlier.
	Target Target
	// File is the exact bytes registration wrote, or would have written. The
	// preview and the written file are the same value, so a preview cannot
	// disagree with what lands on disk.
	File []byte
	// Path is where those bytes belong.
	Path string
	// Changed is false when the file on disk already said this.
	Changed bool
}

// Register confirms a selection against a fresh read of the cluster and writes
// the configuration file.
//
// It re-reads rather than trusting the earlier discovery, and refuses when the
// fingerprint no longer matches. Registering what a person saw and registering
// whatever is there now are different things, and only the first one is honest.
func (s Service) Register(ctx context.Context, root, home string, selection Selection) (Registration, error) {
	registration, err := s.Prepare(ctx, root, home, selection)
	if err != nil {
		return Registration{}, err
	}
	return registration.Apply()
}

// Prepare confirms a selection and renders the exact file without changing
// disk. The caller can show File to a person before calling Apply.
func (s Service) Prepare(ctx context.Context, root, home string, selection Selection) (Registration, error) {
	if err := selection.validate(); err != nil {
		return Registration{}, err
	}

	target, err := s.Inspect(ctx, root, selection.Namespace, selection.Rollout)
	if err != nil {
		return Registration{}, err
	}
	if selection.Fingerprint != target.Fingerprint {
		return Registration{}, release.Invalid("stale_discovery", "fingerprint",
			fmt.Sprintf("namespace %s changed between discovery and registration", selection.Namespace),
			"Run discovery again and confirm what it finds now.")
	}
	if !target.Environment.Supported {
		return Registration{}, unsupportedTarget(target)
	}
	container, ok := target.SelectedContainer(selection.Container)
	if !ok {
		return Registration{}, release.Invalid("unknown_container", "container",
			fmt.Sprintf("Rollout %q has no container called %q", target.Rollout, selection.Container),
			"Choose one of: "+strings.Join(containerNames(target), ", ")+".")
	}
	if selection.Context != "" && selection.Context != target.Context {
		return Registration{}, release.Invalid("context_changed", "context",
			fmt.Sprintf("kubectl now points at %q, not %q", target.Context, selection.Context),
			"Point kubectl back at the cluster you discovered, or run discovery again.")
	}

	discovered := config.Discovered{
		Application: config.Application{Name: selection.Application, Repository: target.Repository},
		Artifact:    config.Artifact{Container: container.Name, Image: ImageRepository(container.Image)},
		Environment: config.Environment{
			Name:   selection.Environment,
			Impact: selection.Impact,
			Kubernetes: config.Kubernetes{
				Context:   target.Context,
				Namespace: target.Namespace,
				Rollout:   target.Rollout,
			},
		},
	}

	locations := config.ForApp(home, selection.Application)
	existing, readErr := os.ReadFile(locations.File)
	if readErr != nil && !os.IsNotExist(readErr) {
		return Registration{}, release.Invalid("unreadable_config", "config",
			fmt.Sprintf("could not read %s: %v", locations.File, readErr),
			"Fix the file's permissions and try again.")
	}

	var file []byte
	if os.IsNotExist(readErr) {
		file = config.Render(discovered, config.DefaultReleaseSettings())
	} else {
		file, err = config.Reconcile(existing, discovered)
		if err != nil {
			return Registration{}, err
		}
	}
	// The preview is the written bytes, so it cannot describe a different
	// file. Parsing it here also means SafeLane never writes something it
	// would then refuse to read.
	if _, err := config.Parse(file); err != nil {
		return Registration{}, err
	}

	changed := readErr != nil || !bytes.Equal(existing, file)
	return Registration{
		Selection: selection,
		Target:    target,
		File:      file,
		Path:      locations.File,
		Changed:   changed,
	}, nil
}

// Apply writes the exact bytes produced by Prepare.
func (r Registration) Apply() (Registration, error) {
	changed, err := config.Write(r.Path, r.File)
	if err != nil {
		return Registration{}, err
	}
	r.Changed = changed
	return r, nil
}

// Analysis names the background analysis registration will watch, and who
// checks it. A1 says both, and "the analysis it will watch" is only meaningful
// if SafeLane can name the one that is actually there.
func (r Registration) Analysis() (name, provider string) {
	for _, analysis := range r.Target.Analysis {
		if analysis.Resolved {
			return analysis.Name, analysis.Provider
		}
	}
	if len(r.Target.Analysis) > 0 {
		return r.Target.Analysis[0].Name, ""
	}
	return "", ""
}

func (s Selection) validate() error {
	var errs release.Errors
	required := map[string]string{
		"application": s.Application,
		"environment": s.Environment,
		"namespace":   s.Namespace,
		"rollout":     s.Rollout,
		"container":   s.Container,
		"fingerprint": s.Fingerprint,
	}
	for field, value := range required {
		if strings.TrimSpace(value) == "" {
			errs = append(errs, release.Invalid("missing_selection_field", field,
				"the selection did not say which "+field+" to register",
				"Run discovery again and confirm every answer."))
		}
	}
	switch s.Impact {
	case config.ImpactLow, config.ImpactSignificant, config.ImpactCritical:
	default:
		errs = append(errs, release.Invalid("invalid_impact", "impact",
			fmt.Sprintf("%q is not an impact level", s.Impact),
			"Say how much this environment matters: low, significant, or critical."))
	}
	return errs.OrNil()
}

func unsupportedTarget(target Target) error {
	var errs release.Errors
	for _, reason := range target.Environment.Reasons {
		errs = append(errs, release.Invalid(reason.Code, "rollout", reason.Explanation,
			"SafeLane does not support this shape. Nothing was changed."))
	}
	if len(errs) == 0 {
		errs = append(errs, release.Invalid("unsupported_rollout", "rollout",
			fmt.Sprintf("Rollout %q is not a shape SafeLane can release", target.Rollout),
			"SafeLane does not support this shape. Nothing was changed."))
	}
	return errs
}

func containerNames(target Target) []string {
	names := make([]string, 0, len(target.Containers))
	for _, container := range target.Containers {
		names = append(names, container.Name)
	}
	return names
}
