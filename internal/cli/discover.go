package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/AndrewMaged814/safelane/internal/discovery"
)

// DiscoverOptions are everything `safelane discover` needs that is not the
// namespace.
type DiscoverOptions struct {
	Root      string
	Namespace string
	// ForceJSON is `--json`, which only matters at a terminal.
	ForceJSON bool
	Service   discovery.Service
}

// Discover reads one namespace and reports what is in it.
//
// It provisions nothing and changes nothing, including the current kubectl
// context. Everything printed here is a description of what was already
// running.
func Discover(ctx context.Context, opts DiscoverOptions, stdout, stderr io.Writer) int {
	found, err := opts.Service.Discover(ctx, opts.Root, opts.Namespace)
	if err != nil {
		return writeResultError(stderr, "discover", err)
	}

	if RenderingFor(stdout, opts.ForceJSON) == RenderJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(found); err != nil {
			return writeResultError(stderr, "discover", err)
		}
		return ExitOK
	}
	fmt.Fprint(stdout, RenderDiscovery(found))
	return ExitOK
}

// RenderDiscovery writes the readable form: what answered, and what it found.
//
// Every Rollout is listed, releasable or not, with its containers and - when
// SafeLane cannot release it - the reason in a sentence. Hiding the ones that
// do not fit would leave a person staring at an empty list wondering whether
// they typed the namespace wrong.
func RenderDiscovery(found discovery.Discovery) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Read namespace %s through context %s.\n", found.Namespace, found.Context)
	if found.Repository != "" {
		fmt.Fprintf(&b, "This repository is %s.\n", found.Repository)
	}

	if len(found.Rollouts) == 0 {
		fmt.Fprintf(&b, "\nNo Argo Rollouts are readable in %s.\n", found.Namespace)
		return b.String()
	}

	for _, rollout := range found.Rollouts {
		fmt.Fprintf(&b, "\nRollout %s\n", rollout.Name)
		if len(rollout.Containers) == 0 {
			b.WriteString("  containers   none declared inline\n")
		}
		for i, container := range rollout.Containers {
			label := "  containers  "
			if i > 0 {
				label = "              "
			}
			fmt.Fprintf(&b, "%s %s  %s\n", label, container.Name, container.Image)
		}
		if rollout.Environment.Supported {
			b.WriteString("  releasable   yes\n")
			continue
		}
		b.WriteString("  releasable   no\n")
		for _, reason := range rollout.Environment.Reasons {
			fmt.Fprintf(&b, "               %s\n", reason.Explanation)
		}
	}
	return b.String()
}
