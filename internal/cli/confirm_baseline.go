package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/AndrewMaged814/safelane/internal/config"
	"github.com/AndrewMaged814/safelane/internal/discovery"
	"github.com/AndrewMaged814/safelane/internal/privatefile"
	"github.com/AndrewMaged814/safelane/internal/release"
	"github.com/AndrewMaged814/safelane/internal/verify/oci"
)

const confirmedBaselineFile = "confirmed-baseline.json"

type confirmedBaseline struct {
	Artifact oci.Artifact       `json:"artifact"`
	Source   oci.SourceMetadata `json:"source"`
}

// ConfirmBaselineOptions are the narrow inputs to the one-time adoption
// adapter. The user names the exact commit already running; SafeLane checks it
// against the registered repository and binds it to the observed digest.
type ConfirmBaselineOptions struct {
	Root        string
	Home        string
	Environment string
	Revision    string
	App         string
	ForceJSON   bool
	Cluster     discovery.Service
	Registry    oci.Resolver
	Checker     oci.RevisionChecker
}

// ConfirmBaseline records one exact, verified baseline for an image whose
// publisher did not include usable source metadata.
func ConfirmBaseline(ctx context.Context, opts ConfirmBaselineOptions, stdout, stderr io.Writer) int {
	application, err := applicationFrom(opts.Root, opts.Home, opts.App, opts.Cluster.Origin)
	if err != nil {
		return writeResultError(stderr, "confirm-baseline", err)
	}
	cfg, err := config.Load(config.ForApp(opts.Home, application).File)
	if err != nil {
		return writeResultError(stderr, "confirm-baseline", err)
	}
	environment, ok := cfg.Environment(opts.Environment)
	if !ok {
		return writeResultError(stderr, "confirm-baseline", unknownEnvironment(application, opts.Environment, cfg))
	}
	target, err := opts.Cluster.Inspect(ctx, opts.Root, environment.Kubernetes.Namespace, environment.Kubernetes.Rollout)
	if err != nil {
		return writeResultError(stderr, "confirm-baseline", err)
	}
	container, found := target.SelectedContainer(cfg.Artifact.Container)
	if !found {
		return writeResultError(stderr, "confirm-baseline", release.Invalid("container_not_found", "artifact.container",
			fmt.Sprintf("Rollout %q no longer has a container called %q", target.Rollout, cfg.Artifact.Container),
			"Register this application again to pick up the change."))
	}
	artifact, err := resolveRunningArtifact(ctx, opts.Registry, cfg.Artifact.Image, container.Image)
	if err != nil {
		return writeResultError(stderr, "confirm-baseline", err)
	}
	metadata, err := opts.Registry.ConfirmBaseline(ctx, opts.Checker, artifact,
		cfg.Application.Repository, opts.Revision)
	if err != nil {
		return writeResultError(stderr, "confirm-baseline", err)
	}

	path := filepath.Join(config.ForApp(opts.Home, application).ForEnvironment(environment.Name).Dir, confirmedBaselineFile)
	existing, loadErr := loadConfirmedBaseline(path)
	if loadErr == nil {
		if existing.Artifact == artifact && existing.Source.Revision == metadata.Revision {
			return renderConfirmedBaseline(stdout, stderr, opts.ForceJSON, artifact, metadata)
		}
		return writeResultError(stderr, "confirm-baseline", release.Invalid("baseline_already_confirmed", "baseline",
			"this environment already has a different confirmed adoption baseline",
			"Use SafeLane release history for later baselines; do not replace the adoption record."))
	}
	if !os.IsNotExist(loadErr) {
		return writeResultError(stderr, "confirm-baseline", loadErr)
	}
	if err := saveConfirmedBaseline(path, confirmedBaseline{Artifact: artifact, Source: metadata}); err != nil {
		return writeResultError(stderr, "confirm-baseline", err)
	}
	return renderConfirmedBaseline(stdout, stderr, opts.ForceJSON, artifact, metadata)
}

func renderConfirmedBaseline(stdout, stderr io.Writer, forceJSON bool, artifact oci.Artifact, metadata oci.SourceMetadata) int {
	if RenderingFor(stdout, forceJSON) == RenderJSON {
		return writeControlJSON(stdout, stderr, "confirm-baseline", map[string]any{
			"confirmed": true, "digest": artifact.Digest, "revision": metadata.Revision,
			"binding_method": metadata.Method,
		})
	}
	fmt.Fprintf(stdout, "Confirmed that the running image came from commit %s.\n", metadata.Revision)
	return ExitOK
}

func loadConfirmedBaseline(path string) (confirmedBaseline, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return confirmedBaseline{}, err
	}
	var baseline confirmedBaseline
	if err := json.Unmarshal(raw, &baseline); err != nil {
		return confirmedBaseline{}, err
	}
	return baseline, nil
}

func saveConfirmedBaseline(path string, baseline confirmedBaseline) error {
	raw, err := json.MarshalIndent(baseline, "", "  ")
	if err != nil {
		return err
	}
	return privatefile.WriteAtomic(path, append(raw, '\n'))
}
