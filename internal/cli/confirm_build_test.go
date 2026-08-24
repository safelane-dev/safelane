package cli

import (
	"bytes"
	"context"
	"testing"

	githubverify "github.com/AndrewMaged814/safelane/internal/verify/github"
)

func TestBuildConfirmationIsBoundToTheCandidateAndDigest(t *testing.T) {
	opts := inspectOptions(t)
	source := defaultInspectSource()
	source.protection = githubverify.Repository{FullName: "acme/payments-api", DefaultBranch: "main"}
	source.checks = githubverify.Checks{Revision: candidateRevision, Workflows: []githubverify.WorkflowRun{
		{ID: 41, Name: "build", Status: "completed", Conclusion: "success", HeadSHA: candidateRevision},
		{ID: 42, Name: "publish", Status: "completed", Conclusion: "success", HeadSHA: candidateRevision},
	}}
	opts.Source = source
	if _, eligibility, err := FreezeDelta(context.Background(), opts); err != nil {
		t.Fatal(err)
	} else if eligibility.Eligible {
		t.Fatal("ambiguous workflows were eligible without confirmation")
	}

	var stdout, stderr bytes.Buffer
	if code := ConfirmBuild(context.Background(), ConfirmBuildOptions{Inspect: opts, RunID: "42"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("confirm build: %s", stderr.String())
	}
	if _, eligibility, err := FreezeDelta(context.Background(), opts); err != nil {
		t.Fatal(err)
	} else if !eligibility.Eligible {
		t.Fatalf("confirmed workflow remained ambiguous: %+v", eligibility)
	}
}
