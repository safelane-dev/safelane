package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/AndrewMaged814/safelane/internal/discovery"
)

func TestDiscoverPrintsTheReadableListing(t *testing.T) {
	svc := paymentsService(paymentsCluster())
	var stdout, stderr bytes.Buffer

	code := Discover(context.Background(), DiscoverOptions{
		Root: ".", Namespace: "payments", Service: svc,
	}, &terminal{&stdout}, &stderr)

	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	assertGolden(t, "discover-one-rollout.txt", stdout.String())
}

// A Rollout SafeLane cannot release is still listed, with the reason. Hiding it
// would leave a person staring at an empty list wondering whether they typed
// the namespace wrong.
func TestDiscoverExplainsWhatItCannotRelease(t *testing.T) {
	cluster := paymentsCluster()
	cluster["get rollouts.argoproj.io -n payments -o json"] = `{"items":[
      {"metadata":{"name":"legacy-api"},
       "spec":{"strategy":{"blueGreen":{"activeService":"legacy-api"}},
               "template":{"spec":{"containers":[{"name":"legacy-api","image":"ghcr.io/acme/legacy-api:v3"}]}}}}
    ]}`
	svc := paymentsService(cluster)

	var stdout, stderr bytes.Buffer
	if code := Discover(context.Background(), DiscoverOptions{
		Root: ".", Namespace: "payments", Service: svc,
	}, &terminal{&stdout}, &stderr); code != ExitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	assertGolden(t, "discover-unsupported.txt", stdout.String())
}

func TestDiscoverSaysSoWhenThereIsNothingThere(t *testing.T) {
	cluster := paymentsCluster()
	cluster["get rollouts.argoproj.io -n payments -o json"] = `{"items":[]}`

	var stdout, stderr bytes.Buffer
	if code := Discover(context.Background(), DiscoverOptions{
		Root: ".", Namespace: "payments", Service: paymentsService(cluster),
	}, &terminal{&stdout}, &stderr); code != ExitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "No Argo Rollouts are readable in payments.") {
		t.Errorf("output = %q", stdout.String())
	}
}

func TestDiscoverIsJSONWhenPiped(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Discover(context.Background(), DiscoverOptions{
		Root: ".", Namespace: "payments", Service: paymentsService(paymentsCluster()),
	}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}

	var found discovery.Discovery
	if err := json.Unmarshal(stdout.Bytes(), &found); err != nil {
		t.Fatalf("piped output is not JSON: %v\n%s", err, stdout.String())
	}
	if found.Context != "safelane-caller-payments-api" {
		t.Errorf("context = %q", found.Context)
	}
	if len(found.Rollouts) != 1 || found.Rollouts[0].Containers[0].Name != "payments-api" {
		t.Errorf("rollouts = %+v", found.Rollouts)
	}
}

func TestDiscoverReportsAnUnreadableNamespace(t *testing.T) {
	cluster := paymentsCluster()
	delete(cluster, "get rollouts.argoproj.io -n payments -o json")

	var stdout, stderr bytes.Buffer
	code := Discover(context.Background(), DiscoverOptions{
		Root: ".", Namespace: "payments", Service: paymentsService(cluster),
	}, &stdout, &stderr)
	if code == ExitOK {
		t.Fatal("discover reported success against a namespace it could not read")
	}
	if !strings.Contains(stderr.String(), "payments") {
		t.Errorf("the failure should name the namespace: %s", stderr.String())
	}
}
