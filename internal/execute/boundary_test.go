package execute

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAssertCapabilitiesUsesEachConfiguredIdentity(t *testing.T) {
	var calls [][]string
	e := New(Config{Namespace: "safelane-demo-api", ControllerKubeconfig: "controller.kubeconfig", ControllerContext: "controller"})
	e.Now = func() time.Time { return time.Date(2026, 8, 20, 14, 26, 0, 0, time.UTC) }
	e.Run = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		calls = append(calls, append([]string{}, args...))
		joined := strings.Join(args, " ")
		switch joined {
		case "--kubeconfig controller.kubeconfig --context controller auth whoami -o json":
			return []byte(`{"status":{"userInfo":{"username":"system:serviceaccount:safelane-demo-api:safelane-controller"}}}`), nil
		case "auth whoami -o json":
			return []byte(`{"status":{"userInfo":{"username":"system:serviceaccount:safelane-demo-api:safelane-caller"}}}`), nil
		case "--kubeconfig controller.kubeconfig --context controller auth can-i patch rollouts.argoproj.io --namespace safelane-demo-api":
			return []byte("yes\n"), nil
		case "auth can-i get rollouts.argoproj.io --namespace safelane-demo-api":
			return []byte("yes\n"), nil
		case "auth can-i patch rollouts.argoproj.io --namespace safelane-demo-api":
			return []byte("no\n"), errors.New("exit status 1")
		default:
			t.Fatalf("unexpected kubectl call: %v", args)
			return nil, nil
		}
	}
	capabilities, err := e.AssertCapabilities(context.Background(),
		"system:serviceaccount:safelane-demo-api:safelane-controller",
		"system:serviceaccount:safelane-demo-api:safelane-caller")
	if err != nil {
		t.Fatal(err)
	}
	// The caller can read the Rollout and cannot patch it. That denial is
	// enforced by Kubernetes, not by SafeLane, and it holds even if SafeLane
	// is bypassed entirely.
	if !capabilities.CallerGetRollouts || capabilities.CallerPatchRollouts {
		t.Fatalf("capabilities = %+v", capabilities)
	}
	wantCallerGet := []string{"auth", "can-i", "get", "rollouts.argoproj.io", "--namespace", "safelane-demo-api"}
	if !reflect.DeepEqual(calls[3], wantCallerGet) {
		t.Fatalf("caller get args = %v, want %v", calls[3], wantCallerGet)
	}
	if len(calls) != 5 {
		t.Fatalf("calls = %v, want two identity checks and three capability checks", calls)
	}
}

func TestAssertCapabilitiesRejectsUnexpectedConfiguredIdentity(t *testing.T) {
	e := New(Config{Namespace: "safelane-demo-api", ControllerKubeconfig: "controller.kubeconfig", ControllerContext: "controller"})
	e.Run = func(_ context.Context, _ []string, _ []byte) ([]byte, error) {
		return []byte(`{"status":{"userInfo":{"username":"system:serviceaccount:safelane-demo-api:someone-else"}}}`), nil
	}
	_, err := e.AssertCapabilities(context.Background(),
		"system:serviceaccount:safelane-demo-api:safelane-controller",
		"system:serviceaccount:safelane-demo-api:safelane-caller")
	if err == nil || !strings.Contains(err.Error(), "unexpected Kubernetes identity") {
		t.Fatalf("error = %v, want unexpected identity", err)
	}
}
