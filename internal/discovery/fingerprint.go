package discovery

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// fingerprint covers exactly the facts registration writes down, and nothing
// else.
//
// The point is not tamper detection - it is that "register what I just showed
// you" means the thing the person saw. If the namespace moved between the
// listing and the confirmation, registration should say so rather than write
// down whichever version happened to be there second.
//
// Deliberately excluded: resourceVersion, generation, and status. A Rollout
// whose replica count drifted while somebody read the screen is still the same
// Rollout, and a fingerprint that changed every few seconds would train people
// to ignore it.
func fingerprint(t Target) string {
	var b strings.Builder
	fmt.Fprintf(&b, "namespace=%s\n", t.Namespace)
	fmt.Fprintf(&b, "rollout=%s\n", t.Rollout)
	fmt.Fprintf(&b, "stable=%s\ncanary=%s\n", t.StableService, t.CanaryService)
	for _, container := range t.Containers {
		fmt.Fprintf(&b, "container=%s image=%s\n", container.Name, container.Image)
	}
	for _, analysis := range t.Analysis {
		fmt.Fprintf(&b, "analysis=%s resolved=%t\n", analysis.Name, analysis.Resolved)
	}
	fmt.Fprintf(&b, "environment_supported=%t\n", t.Environment.Supported)
	for _, reason := range t.Environment.Reasons {
		fmt.Fprintf(&b, "environment_reason=%s\n", reason.Code)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return "sha256:" + hex.EncodeToString(sum[:])
}
