package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/AndrewMaged814/safelane/internal/release"
)

// Runner runs one kubectl invocation and returns its stdout. It is the single
// seam every read in this package goes through, so discovery is testable
// against canned cluster output with no cluster.
type Runner func(ctx context.Context, args []string) ([]byte, error)

// RealRunner shells out to the kubectl binary on PATH.
//
// Every argument list this package builds is a read. Nothing here calls
// `config use-context`, `apply`, `patch`, `create`, or `delete`, and
// [Service.commandsIssued] is asserted against that in the tests, because "we
// never change your context" is a promise worth keeping structurally rather
// than by review.
func RealRunner(ctx context.Context, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return stdout.Bytes(), fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return stdout.Bytes(), err
	}
	return stdout.Bytes(), nil
}

// rolloutList is the shape of `kubectl get rollouts.argoproj.io -o json`.
type rolloutList struct {
	Items []rolloutDoc `json:"items"`
}

// rolloutDoc is the subset of a Rollout discovery reads. Everything outside it
// is deliberately not modelled: SafeLane preserves fields it does not
// understand, and the way to preserve them is not to have an opinion.
type rolloutDoc struct {
	Metadata objectMeta  `json:"metadata"`
	Spec     rolloutSpec `json:"spec"`
}

type objectMeta struct {
	Name        string            `json:"name"`
	Namespace   string            `json:"namespace"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
}

type rolloutSpec struct {
	// WorkloadRef points the pod template at a separate Deployment. SafeLane
	// patches the Rollout's own template, so a Rollout that has none is not
	// something it can release.
	WorkloadRef *struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	} `json:"workloadRef"`
	Strategy strategy    `json:"strategy"`
	Template podTemplate `json:"template"`
}

type strategy struct {
	BlueGreen *json.RawMessage `json:"blueGreen"`
	Canary    *canary          `json:"canary"`
}

type canary struct {
	StableService  string          `json:"stableService"`
	CanaryService  string          `json:"canaryService"`
	TrafficRouting map[string]any  `json:"trafficRouting"`
	Analysis       *canaryAnalysis `json:"analysis"`
	Steps          []canaryStep    `json:"steps"`
}

type canaryAnalysis struct {
	Templates []templateRef `json:"templates"`
	Args      []analysisArg `json:"args"`
}

type templateRef struct {
	TemplateName string `json:"templateName"`
	ClusterScope bool   `json:"clusterScope"`
}

type analysisArg struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// canaryStep models only the two step shapes SafeLane can regenerate. Every
// other key is captured so an unsupported shape can be named rather than
// ignored.
type canaryStep map[string]json.RawMessage

type podTemplate struct {
	Spec struct {
		Containers []containerDoc `json:"containers"`
	} `json:"spec"`
}

type containerDoc struct {
	Name  string `json:"name"`
	Image string `json:"image"`
}

// analysisTemplateDoc is the subset of an AnalysisTemplate discovery reads: its
// name, and which providers its metrics ask. The provider is what lets
// registration say "checked by Prometheus" instead of "checked somehow".
type analysisTemplateDoc struct {
	Metadata objectMeta `json:"metadata"`
	Spec     struct {
		Metrics []struct {
			Name             string         `json:"name"`
			Interval         string         `json:"interval"`
			InitialDelay     string         `json:"initialDelay"`
			SuccessCondition string         `json:"successCondition"`
			FailureCondition string         `json:"failureCondition"`
			FailureLimit     int            `json:"failureLimit"`
			Count            int            `json:"count"`
			Provider         map[string]any `json:"provider"`
		} `json:"metrics"`
	} `json:"spec"`
}

func getJSON[T any](ctx context.Context, run Runner, args []string) (T, error) {
	var out T
	raw, err := run(ctx, args)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, release.Invalid("unreadable_cluster_response", "kubernetes",
			fmt.Sprintf("kubectl %s did not return readable JSON: %v", strings.Join(args, " "), err),
			"Check that kubectl points at a working cluster and try again.")
	}
	return out, nil
}
