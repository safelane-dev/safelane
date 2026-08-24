package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/AndrewMaged814/safelane/internal/release"
)

// Discovered is everything registration observed and a person confirmed. It is
// deliberately not a whole [Config]: registration has no opinion about release settings,
// so it cannot express one.
type Discovered struct {
	Application Application
	Artifact    Artifact
	Environment Environment
}

// Render produces the complete file for an Application that has none yet.
func Render(d Discovered, settings ReleaseSettings) []byte {
	var b strings.Builder
	b.WriteString(renderApplication(d.Application))
	b.WriteString("\n")
	b.WriteString(renderArtifact(d.Artifact))
	b.WriteString("\n")
	b.WriteString(renderEnvironments([]Environment{d.Environment}))
	b.WriteString("\n")
	b.WriteString(renderReleaseSettings(settings))
	return []byte(b.String())
}

// Reconcile folds newly discovered facts into the file that is already there
// and returns the bytes that should replace it.
//
// Two things are preserved, and they are the whole point of reconciling rather
// than rewriting:
//
//   - the release-settings block is carried across byte-for-byte, comments and hand-edited
//     weights included, because discovery never had an opinion about it;
//   - Environments are matched by name, so re-registering one leaves every
//     other one exactly as it was.
//
// The existing file must still be a valid configuration. Reconciling onto
// something SafeLane cannot read would mean guessing which half to keep.
func Reconcile(existing []byte, d Discovered) ([]byte, error) {
	if len(bytes.TrimSpace(existing)) == 0 {
		return Render(d, DefaultReleaseSettings()), nil
	}
	current, err := Parse(existing)
	if err != nil {
		return nil, err
	}

	environments := append([]Environment(nil), current.Environments...)
	replaced := false
	for i, env := range environments {
		if env.Name == d.Environment.Name {
			environments[i] = d.Environment
			replaced = true
			break
		}
	}
	if !replaced {
		environments = append(environments, d.Environment)
	}

	policyBlock, err := policyBlockOf(existing)
	if err != nil {
		return nil, err
	}

	var b strings.Builder
	b.WriteString(renderApplication(d.Application))
	b.WriteString("\n")
	b.WriteString(renderArtifact(d.Artifact))
	b.WriteString("\n")
	b.WriteString(renderEnvironments(environments))
	b.WriteString("\n")
	b.WriteString(policyBlock)
	return []byte(b.String()), nil
}

// Write replaces the file at path with next, and reports whether anything
// changed.
//
// Identical bytes write nothing at all: no temporary file, no rename, no
// modification time. Registering twice with nothing new to say is a no-op the
// filesystem can confirm.
//
// A change goes through a temporary file in the same directory and a rename, so
// an interrupted write leaves the previous configuration intact rather than
// half of a new one.
func Write(path string, next []byte) (changed bool, err error) {
	existing, readErr := os.ReadFile(path)
	if readErr == nil && bytes.Equal(existing, next) {
		return false, nil
	}
	if readErr != nil && !os.IsNotExist(readErr) {
		return false, release.Invalid("unreadable_config", "config",
			fmt.Sprintf("could not read %s: %v", path, readErr),
			"Fix the file's permissions and try again.")
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, fmt.Errorf("create %s: %w", dir, err)
	}
	temp, err := os.CreateTemp(dir, ".safelane.yml.*")
	if err != nil {
		return false, fmt.Errorf("create a temporary file in %s: %w", dir, err)
	}
	tempName := temp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tempName)
		}
	}()

	if _, err = temp.Write(next); err != nil {
		temp.Close()
		return false, fmt.Errorf("write %s: %w", tempName, err)
	}
	if err = temp.Close(); err != nil {
		return false, fmt.Errorf("write %s: %w", tempName, err)
	}
	if err = os.Chmod(tempName, 0o600); err != nil {
		return false, fmt.Errorf("set permissions on %s: %w", tempName, err)
	}
	if err = os.Rename(tempName, path); err != nil {
		return false, fmt.Errorf("replace %s: %w", path, err)
	}
	return true, nil
}

// policyBlockOf returns the serialized release-settings block exactly as it was written.
//
// The node tree locates it - which line the `policy:` key is on, and which line
// the next top-level key starts - and the bytes between those lines are copied
// across untouched. Re-encoding the node would round-trip the user's
// formatting through yaml.v3's opinions about indentation and quoting, and
// "your lanes are still there, just reformatted" is not the promise.
func policyBlockOf(raw []byte) (string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil || len(doc.Content) == 0 {
		return "", release.Malformed("unreadable_config", "config",
			"this is not a SafeLane configuration file",
			"Register this application again.")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return "", release.Malformed("unreadable_config", "config",
			"this is not a SafeLane configuration file",
			"Register this application again.")
	}

	startLine, endLine := 0, 0
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i]
		if key.Value != "policy" {
			continue
		}
		startLine = key.Line
		if i+2 < len(root.Content) {
			endLine = root.Content[i+2].Line
		}
		break
	}
	if startLine == 0 {
		return "", release.Invalid("missing_config_field", "policy",
			"the configuration file declares no policy",
			"Register this application again to restore the default lanes.")
	}

	lines := strings.Split(string(raw), "\n")
	if endLine == 0 || endLine > len(lines) {
		endLine = len(lines) + 1
	}
	block := strings.Join(lines[startLine-1:endLine-1], "\n")
	return strings.TrimRight(block, " \t\n") + "\n", nil
}

func renderApplication(a Application) string {
	var b strings.Builder
	b.WriteString("application:\n")
	b.WriteString("  name: " + scalar(a.Name) + "\n")
	b.WriteString("  repository: " + scalar(a.Repository) + "\n")
	return b.String()
}

func renderArtifact(a Artifact) string {
	var b strings.Builder
	b.WriteString("artifact:\n")
	b.WriteString("  container: " + scalar(a.Container) + "\n")
	b.WriteString("  image: " + scalar(a.Image) + "\n")
	return b.String()
}

func renderEnvironments(environments []Environment) string {
	var b strings.Builder
	b.WriteString("environments:\n")
	for _, env := range environments {
		b.WriteString("  - name: " + scalar(env.Name) + "\n")
		b.WriteString("    impact: " + scalar(string(env.Impact)) + "\n")
		b.WriteString("    kubernetes:\n")
		b.WriteString("      context: " + scalar(env.Kubernetes.Context) + "\n")
		b.WriteString("      namespace: " + scalar(env.Kubernetes.Namespace) + "\n")
		b.WriteString("      rollout: " + scalar(env.Kubernetes.Rollout) + "\n")
	}
	return b.String()
}

func renderReleaseSettings(p ReleaseSettings) string {
	var b strings.Builder
	b.WriteString("policy:\n")
	b.WriteString("  default_lane: " + scalar(p.DefaultLane) + "\n")
	b.WriteString("  risk_mapping:\n")
	for _, risk := range Risks {
		b.WriteString("    " + string(risk) + ": " + scalar(p.RiskMapping[risk]) + "\n")
	}
	b.WriteString("  lanes:\n")
	for _, name := range laneOrder(p) {
		b.WriteString("    " + name + ":\n")
		b.WriteString("      weights: " + weightList(p.Lanes[name].Weights) + "\n")
	}
	return b.String()
}

// laneOrder lists lanes in the order risk escalates - the lane for low risk
// first, then medium, then high - falling back to alphabetical for any lane
// nothing maps to. Read top to bottom the file then goes from the widest lane
// to the most cautious one, which is the order a person thinks about them in.
func laneOrder(p ReleaseSettings) []string {
	seen := make(map[string]bool, len(p.Lanes))
	order := make([]string, 0, len(p.Lanes))
	for _, risk := range Risks {
		name := p.RiskMapping[risk]
		if _, declared := p.Lanes[name]; declared && !seen[name] {
			seen[name] = true
			order = append(order, name)
		}
	}
	for _, name := range p.LaneNames() {
		if !seen[name] {
			order = append(order, name)
		}
	}
	return order
}

func weightList(weights []int) string {
	parts := make([]string, 0, len(weights))
	for _, weight := range weights {
		parts = append(parts, strconv.Itoa(weight))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// scalar quotes a value only when leaving it bare would change what YAML reads
// back. Registration writes ordinary names almost every time, and quoting them
// all would make the file look machine-generated for no benefit.
func scalar(value string) string {
	if value == "" {
		return `""`
	}
	if value != strings.TrimSpace(value) {
		return strconv.Quote(value)
	}
	if strings.ContainsAny(value, ":#{}[]&*!|>'\"%@`,") {
		return strconv.Quote(value)
	}
	switch strings.ToLower(value) {
	case "true", "false", "null", "yes", "no", "on", "off", "~":
		return strconv.Quote(value)
	}
	if _, err := strconv.ParseFloat(value, 64); err == nil {
		return strconv.Quote(value)
	}
	return value
}
