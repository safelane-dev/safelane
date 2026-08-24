package assessment

import (
	"fmt"
	"strings"

	"github.com/AndrewMaged814/safelane/internal/config"
)

// The rendered text is fixed by Appendix A so that a dozen separate
// implementation sessions produce one voice. The wording, order and labels here
// are copied from the plan; the values are the release's own.
//
// What is deliberately absent: risk labels, recommendation and release
// identifiers, hazard identifiers, schema versions, hashes, commit
// placeholders, and commands. Those are all real and all recorded in proof.
// They are not in the sentence a person reads, because a person deciding
// whether to ship something is not helped by being handed the filing system.

// RenderProceeding is Appendix A2, followed by A5.
//
// A5 is not optional and never appears alone: the approval question is only
// meaningful next to the reasons, and the reasons are only actionable next to
// the question.
func RenderProceeding(r Recommendation, environment string, lane config.Lane) string {
	return renderA2(r, lane) + "\n" + RenderApprovalQuestion(environment)
}

func renderA2(r Recommendation, lane config.Lane) string {
	var b strings.Builder
	b.WriteString("I recommend proceeding with this release.\n\n")

	b.WriteString("Why it looks ready\n")
	for _, observation := range r.Observations {
		b.WriteString("✓ " + observation.Statement + "\n")
	}

	b.WriteString("\nProposed rollout\n")
	b.WriteString(describeRollout(lane.Weights) + "\n")
	fmt.Fprintf(&b, "Using your %s lane.\n", r.Lane)

	b.WriteString("\nSafeLane will stay with the rollout. If the checks fail, Argo will stop it and restore the stable version.\n")
	return b.String()
}

// describeRollout turns configured weights into the sentence A2 fixes.
//
// Two weights read exactly as the appendix does. More weights add a stop each,
// in the same shape, rather than switching to a list - the point of the
// sentence is that a person can hear how far their change goes before anybody
// looks at it.
func describeRollout(weights []int) string {
	if len(weights) < 2 {
		return "Release to 100% in one step."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Release to %d%%, verify the configured health analysis", weights[0])
	for _, weight := range weights[1 : len(weights)-1] {
		fmt.Fprintf(&b, ", then %d%%, verify again", weight)
	}
	fmt.Fprintf(&b, ", then continue to %d%%.", weights[len(weights)-1])
	return b.String()
}

// RenderApprovalQuestion is Appendix A5.
//
// It states what SafeLane will not touch before it asks, because that is the
// part that makes the answer safe to give. A question that only says what will
// change invites a person to imagine the rest.
func RenderApprovalQuestion(environment string) string {
	return "I will change the container image and the canary steps. Your probes, resources, replicas,\n" +
		"environment, secrets, ports, Services, traffic router, and health analysis stay exactly as\n" +
		"they are.\n" +
		"\n" +
		fmt.Sprintf("Proceed with this rollout to %s?\n", environment)
}

// RenderWaiting is Appendix A3.
//
// Four sections, in this order, because they answer the four questions a person
// actually has: what is wrong, what is unknown, what would not be caught, and
// what to do about it. A waiting recommendation that stopped after the first
// one would be a complaint.
func RenderWaiting(r Recommendation) string {
	var b strings.Builder
	b.WriteString("I recommend waiting on this release.\n\n")

	b.WriteString("What concerns me\n")
	b.WriteString(paragraph(firstNonEmpty(r.Concern, hazardConcerns(r), r.Rationale)))

	b.WriteString("\nWhat I could not confirm\n")
	b.WriteString(paragraph(firstNonEmpty(r.Unconfirmed, "Nothing further; the concern above is the whole of it.")))

	b.WriteString("\nWhat your health analysis would not catch\n")
	b.WriteString(paragraph(firstNonEmpty(r.Blindspot, coverageBlindspots(r),
		"I could not tell what your configured analysis would catch here.")))

	b.WriteString("\nNext step\n")
	b.WriteString(paragraph(r.NextStep))
	return b.String()
}

// hazardConcerns falls back to the hazards' own consequences when the
// recommendation did not write a concern of its own. The consequence is the
// part a person needs: not that a hazard exists, but what happens if it lands.
func hazardConcerns(r Recommendation) string {
	parts := make([]string, 0, len(r.Hazards))
	for _, hazard := range r.Hazards {
		if strings.TrimSpace(hazard.Consequence) != "" {
			parts = append(parts, hazard.Consequence)
		}
	}
	return strings.Join(parts, "\n\n")
}

func coverageBlindspots(r Recommendation) string {
	parts := make([]string, 0, len(r.Hazards))
	for _, hazard := range r.Hazards {
		switch hazard.Coverage.Status {
		case CoverageNone, CoveragePartial, CoverageUnknown:
			if strings.TrimSpace(hazard.Coverage.Explanation) != "" {
				parts = append(parts, hazard.Coverage.Explanation)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

func paragraph(text string) string {
	trimmed := strings.TrimRight(text, "\n")
	if trimmed == "" {
		return "\n"
	}
	return trimmed + "\n"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
