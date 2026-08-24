package skill

import (
	"strings"
	"testing"
)

func TestAssessmentInstructionsKeepAgentInsideFrozenEvidence(t *testing.T) {
	text := string(SafeLane)
	for _, want := range []string{
		"Run every SafeLane command from the Application repository root",
		"registration_candidates",
		"Kubernetes, registry, and CI inspection are outside an assessment",
		"remove the unsupported claim",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("skill is missing %q", want)
		}
	}
}
