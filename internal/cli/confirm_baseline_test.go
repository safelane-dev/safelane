package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/AndrewMaged814/safelane/internal/verify/oci"
)

type adoptionRegistry struct{ inspectRegistry }

func (adoptionRegistry) Platforms(_ context.Context, _, digest string) ([]oci.PlatformLabels, error) {
	if digest == runningDigest {
		return []oci.PlatformLabels{{Platform: "linux/amd64", Labels: map[string]string{}}}, nil
	}
	return inspectRegistry{}.Platforms(context.Background(), "", digest)
}

type adoptionChecker struct{}

func (adoptionChecker) RevisionExists(_ context.Context, _ string, revision string) (bool, error) {
	return revision == runningRevision, nil
}

func TestConfirmedAdoptionBaselineIsUsedByTheNextInspection(t *testing.T) {
	opts := inspectOptions(t)
	registry := oci.Resolver{Registry: adoptionRegistry{}}
	var stdout, stderr bytes.Buffer
	code := ConfirmBaseline(context.Background(), ConfirmBaselineOptions{
		Root: opts.Root, Home: opts.Home, Environment: opts.Environment,
		Cluster: opts.Cluster, Registry: registry, Checker: adoptionChecker{},
		Revision: runningRevision,
	}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("confirm baseline: %s", stderr.String())
	}

	opts.Registry = registry
	frozen, eligibility, err := FreezeDelta(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !eligibility.Eligible {
		t.Fatalf("confirmed baseline remained ineligible: %+v", eligibility)
	}
	if frozen.Baseline().Revision != runningRevision || frozen.Baseline().Method != string(oci.BindingConfirmedBaseline) {
		t.Fatalf("baseline = %+v", frozen.Baseline())
	}
}
