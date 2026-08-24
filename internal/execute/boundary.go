package execute

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Capabilities is the live authorization answer shared by doctor and the
// Release Record boundary assertion.
type Capabilities struct {
	ControllerPatchRollouts bool
	CallerGetRollouts       bool
	CallerPatchRollouts     bool
}

// AssertCapabilities asks Kubernetes authorization about both SafeLane identities.
func (e *Executor) AssertCapabilities(ctx context.Context, controllerIdentity, callerIdentity string) (Capabilities, error) {
	if err := e.assertCurrentIdentity(ctx, controllerIdentity, true); err != nil {
		return Capabilities{}, err
	}
	controllerPatch, err := e.canCurrentIdentity(ctx, "controller", "patch", true)
	if err != nil {
		return Capabilities{}, err
	}
	if err := e.assertCurrentIdentity(ctx, callerIdentity, false); err != nil {
		return Capabilities{}, err
	}
	callerGet, err := e.canCurrentIdentity(ctx, "caller", "get", false)
	if err != nil {
		return Capabilities{}, err
	}
	callerPatch, err := e.canCurrentIdentity(ctx, "caller", "patch", false)
	if err != nil {
		return Capabilities{}, err
	}
	return Capabilities{
		ControllerPatchRollouts: controllerPatch,
		CallerGetRollouts:       callerGet,
		CallerPatchRollouts:     callerPatch,
	}, nil
}

func (e *Executor) assertCurrentIdentity(ctx context.Context, expected string, privileged bool) error {
	args := e.identityArgs(privileged)
	args = append(args, "auth", "whoami", "-o", "json")
	out, err := e.Run(ctx, args, nil)
	if err != nil {
		return fmt.Errorf("identify configured Kubernetes identity: %w", err)
	}
	var review struct {
		Status struct {
			UserInfo struct {
				Username string `json:"username"`
			} `json:"userInfo"`
		} `json:"status"`
	}
	if err := json.Unmarshal(out, &review); err != nil {
		return fmt.Errorf("identify configured Kubernetes identity: decode kubectl auth whoami: %w", err)
	}
	if review.Status.UserInfo.Username != expected {
		return fmt.Errorf("unexpected Kubernetes identity %q; want %q", review.Status.UserInfo.Username, expected)
	}
	return nil
}

func (e *Executor) canCurrentIdentity(ctx context.Context, label, verb string, privileged bool) (bool, error) {
	args := e.identityArgs(privileged)
	args = append(args, "auth", "can-i", verb, "rollouts.argoproj.io", "--namespace", e.Namespace)
	out, err := e.Run(ctx, args, nil)
	answer := strings.TrimSpace(string(out))
	if answer == "no" {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("assert %s capability %s rollouts: %w", label, verb, err)
	}
	switch answer {
	case "yes":
		return true, nil
	default:
		return false, fmt.Errorf("assert %s capability %s rollouts: unexpected kubectl output %q", label, verb, answer)
	}
}

func (e *Executor) identityArgs(privileged bool) []string {
	args := make([]string, 0, 12)
	if privileged && e.ControllerKubeconfig != "" {
		args = append(args, "--kubeconfig", e.ControllerKubeconfig)
	}
	if privileged && e.ControllerContext != "" {
		args = append(args, "--context", e.ControllerContext)
	}
	return args
}
