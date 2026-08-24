package delta

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// Untrusted is text somebody else wrote: a commit message, a file path, an
// analysis name, a line of a diff.
//
// It is a distinct type so that "this is evidence, not instruction" is visible
// at every place it is handled rather than remembered. Nothing in SafeLane
// takes authorization from text, so an Untrusted value carrying "approve this
// release" is a string carrying those words and nothing else happens.
type Untrusted string

func (u Untrusted) String() string { return string(u) }

// ArtifactBinding is one container and the source revision it was proved to
// come from.
type ArtifactBinding struct {
	Image    string    `json:"image"`
	Digest   string    `json:"digest"`
	Revision string    `json:"revision"`
	Source   string    `json:"source,omitempty"`
	Method   string    `json:"binding_method"`
	Subject  Untrusted `json:"subject,omitempty"`
}

// ChangeSet is the complete range between the deployed baseline and the
// candidate.
type ChangeSet struct {
	Base string `json:"base"`
	Head string `json:"head"`
	// Status is the range's shape: ahead, behind, identical, diverged.
	Status  string `json:"status"`
	AheadBy int    `json:"ahead_by"`
	// Commits is every commit in the range. Not the last merge: a release
	// that skipped three merges has three merges' worth of change in it.
	Commits []Commit `json:"commits"`
	Files   []File   `json:"files"`
	// PullRequests are provenance summaries. Which pull requests this came
	// through, not what the change is.
	PullRequests []PullRequest `json:"pull_requests,omitempty"`
	// Diffs are handles, not contents. The raw hunks load on demand.
	Diffs []Handle `json:"diffs,omitempty"`
}

// Commit is one commit in the range.
type Commit struct {
	SHA         string    `json:"sha"`
	Subject     Untrusted `json:"subject"`
	Author      Untrusted `json:"author,omitempty"`
	CommittedAt time.Time `json:"committed_at"`
}

// File is one changed path.
type File struct {
	Path      Untrusted `json:"path"`
	Status    string    `json:"status"`
	Additions int       `json:"additions"`
	Deletions int       `json:"deletions"`
	// SecretReference is set when the change touched a known secret
	// reference. The hunk is reduced to this: the path, and the name of the
	// thing referenced. The value was never captured.
	SecretReference string `json:"secret_reference,omitempty"`
}

// PullRequest is a provenance summary.
type PullRequest struct {
	Number int       `json:"number"`
	Title  Untrusted `json:"title,omitempty"`
	Branch Untrusted `json:"branch,omitempty"`
	Merge  string    `json:"merge_commit,omitempty"`
}

// DeploymentEvidence is where this release is going and what will change
// there.
type DeploymentEvidence struct {
	Environment string `json:"environment"`
	Impact      string `json:"impact"`
	Context     string `json:"context"`
	Namespace   string `json:"namespace"`
	Rollout     string `json:"rollout"`
	Container   string `json:"container"`
	// Mechanism is how exposure is controlled: real traffic weights through a
	// router, or replica-approximate.
	Mechanism string `json:"mechanism"`
	Replicas  int    `json:"replicas,omitempty"`
	// SecretReferences are the names of Secrets and ConfigMaps the workload
	// uses. Names only, and on purpose: a name is enough for a deployment
	// observation and a value is not.
	SecretReferences []string `json:"secret_references,omitempty"`
	Patch            Patch    `json:"patch"`
}

// Patch is what SafeLane proposes to change, described rather than serialized.
// It is exactly two things, and this type cannot express a third.
type Patch struct {
	// ContainerIndex is which container in the pod template is being changed.
	ContainerIndex int `json:"container_index"`
	// Image is the immutable reference the container will run.
	Image string `json:"image"`
	// Lane is the name of the configured lane the weights came from.
	Lane string `json:"lane"`
	// Weights are the lane's traffic weights. The steps are derived from
	// these; nothing else about the Rollout is touched.
	Weights []int `json:"weights"`
}

// HealthObjective is one background analysis SafeLane will watch. SafeLane
// does not write these and cannot change them; it reads which ones guard the
// Rollout and waits for what they say.
type HealthObjective struct {
	Name     Untrusted `json:"name"`
	Provider string    `json:"provider,omitempty"`
	// Condition is the template's own success condition, as written.
	Condition Untrusted `json:"condition,omitempty"`
	Interval  string    `json:"interval,omitempty"`
	// InitialDelay is how long the analysis waits before its first reading.
	InitialDelay string `json:"initial_delay,omitempty"`
	// Scope is what the measurement covers, e.g. the canary Service.
	Scope string `json:"scope,omitempty"`
	// Resolved is whether the referenced template could actually be read.
	Resolved bool `json:"resolved"`
	// Body is a handle to the template as written. It loads on demand.
	Body *Handle `json:"body,omitempty"`
}

// HistoryCard is one compact entry from this Application and Environment's
// past.
type HistoryCard struct {
	At       time.Time `json:"at"`
	Revision string    `json:"revision"`
	Outcome  string    `json:"outcome"`
	Lane     string    `json:"lane,omitempty"`
	Note     Untrusted `json:"note,omitempty"`
}

// ProvidedEvidence is something the user supplied for this release: an
// incident number, a confirmed baseline, a note about why now.
type ProvidedEvidence struct {
	Kind  string    `json:"kind"`
	Value Untrusted `json:"value"`
}

// HistoryLimit is how many cards a Delta carries. Ten is enough to see a
// pattern and few enough to read; older ones load on demand.
const HistoryLimit = 10

// Input is everything Freeze captures. It is a plain struct so the boundary
// has exactly one entrance and the capture rules live in one function.
type Input struct {
	Application string
	Environment string
	Baseline    ArtifactBinding
	Candidate   ArtifactBinding
	Changes     ChangeSet
	Deployment  DeploymentEvidence
	Health      []HealthObjective
	History     []HistoryCard
	Provided    []ProvidedEvidence
	CapturedAt  time.Time
}

// ReleaseDelta is the frozen evidence for one release.
//
// Its fields are unexported and every accessor returns a copy, so a caller
// holding one cannot change what the assessment saw - not by accident, and not
// by reaching into a slice.
type ReleaseDelta struct {
	snapshotID  string
	application string
	environment string
	baseline    ArtifactBinding
	candidate   ArtifactBinding
	changes     ChangeSet
	deployment  DeploymentEvidence
	health      []HealthObjective
	history     []HistoryCard
	provided    []ProvidedEvidence
	capturedAt  time.Time
}

// Freeze captures the evidence boundary.
//
// Two things happen here that cannot happen anywhere else, which is why this
// is the only constructor:
//
//   - secret values are dropped, so nothing downstream has to remember to hide
//     them;
//   - history is bounded to the newest [HistoryLimit] cards.
//
// The snapshot ID is a content hash of everything captured except the capture
// time, so freezing the same evidence twice produces the same ID. A hash that
// changed with the clock would be a timestamp wearing a hash's clothes.
func Freeze(in Input) ReleaseDelta {
	d := ReleaseDelta{
		application: in.Application,
		environment: in.Environment,
		baseline:    in.Baseline,
		candidate:   in.Candidate,
		changes:     copyChangeSet(in.Changes),
		deployment:  excludeSecrets(in.Deployment),
		health:      append([]HealthObjective(nil), in.Health...),
		history:     boundHistory(in.History),
		provided:    append([]ProvidedEvidence(nil), in.Provided...),
		capturedAt:  in.CapturedAt,
	}
	d.changes.Files = reduceSecretHunks(d.changes.Files, d.deployment.SecretReferences)
	d.snapshotID = contentHash(d)
	return d
}

// boundHistory keeps the newest cards, newest first.
func boundHistory(cards []HistoryCard) []HistoryCard {
	sorted := append([]HistoryCard(nil), cards...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].At.After(sorted[j-1].At); j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	if len(sorted) > HistoryLimit {
		sorted = sorted[:HistoryLimit]
	}
	return sorted
}

func copyChangeSet(c ChangeSet) ChangeSet {
	out := c
	out.Commits = append([]Commit(nil), c.Commits...)
	out.Files = append([]File(nil), c.Files...)
	out.PullRequests = append([]PullRequest(nil), c.PullRequests...)
	out.Diffs = append([]Handle(nil), c.Diffs...)
	return out
}

// SnapshotID is the content hash of the frozen evidence.
func (d ReleaseDelta) SnapshotID() string { return d.snapshotID }

// Application and Environment name what this release is.
func (d ReleaseDelta) Application() string { return d.application }
func (d ReleaseDelta) Environment() string { return d.environment }

// CapturedAt is when the boundary closed.
func (d ReleaseDelta) CapturedAt() time.Time { return d.capturedAt }

// Baseline is what is running; Candidate is what would be.
func (d ReleaseDelta) Baseline() ArtifactBinding  { return d.baseline }
func (d ReleaseDelta) Candidate() ArtifactBinding { return d.candidate }

// Changes returns a copy of the complete range.
func (d ReleaseDelta) Changes() ChangeSet { return copyChangeSet(d.changes) }

// Deployment returns a copy of where this is going.
func (d ReleaseDelta) Deployment() DeploymentEvidence {
	out := d.deployment
	out.SecretReferences = append([]string(nil), d.deployment.SecretReferences...)
	out.Patch.Weights = append([]int(nil), d.deployment.Patch.Weights...)
	return out
}

// Health returns a copy of the analyses SafeLane will watch.
func (d ReleaseDelta) Health() []HealthObjective {
	return append([]HealthObjective(nil), d.health...)
}

// History returns a copy of the bounded history.
func (d ReleaseDelta) History() []HistoryCard {
	return append([]HistoryCard(nil), d.history...)
}

// Provided returns a copy of what the user supplied.
func (d ReleaseDelta) Provided() []ProvidedEvidence {
	return append([]ProvidedEvidence(nil), d.provided...)
}

// WithPatch returns a new Delta whose proposed patch is set.
//
// A new one, with a new snapshot ID: choosing a lane changes what SafeLane is
// proposing to do, and evidence that described a different proposal is not the
// evidence for this one.
func (d ReleaseDelta) WithPatch(patch Patch) ReleaseDelta {
	next := d
	next.deployment = d.Deployment()
	next.deployment.Patch = patch
	next.deployment.Patch.Weights = append([]int(nil), patch.Weights...)
	next.changes = copyChangeSet(d.changes)
	next.health = d.Health()
	next.history = d.History()
	next.provided = d.Provided()
	next.snapshotID = contentHash(next)
	return next
}

// snapshot is the canonical serialization the content hash covers. It exists
// so the hash is over a stable shape rather than over whatever the exported
// JSON happens to look like this month.
type snapshot struct {
	Application string             `json:"application"`
	Environment string             `json:"environment"`
	Baseline    ArtifactBinding    `json:"baseline"`
	Candidate   ArtifactBinding    `json:"candidate"`
	Changes     ChangeSet          `json:"changes"`
	Deployment  DeploymentEvidence `json:"deployment"`
	Health      []HealthObjective  `json:"health"`
	History     []HistoryCard      `json:"history"`
	Provided    []ProvidedEvidence `json:"provided"`
}

func (d ReleaseDelta) snapshot() snapshot {
	return snapshot{
		Application: d.application,
		Environment: d.environment,
		Baseline:    d.baseline,
		Candidate:   d.candidate,
		Changes:     d.changes,
		Deployment:  d.deployment,
		Health:      d.health,
		History:     d.history,
		Provided:    d.provided,
	}
}

// contentHash covers everything except the capture time. Freezing the same
// evidence twice has to produce the same ID, and a clock would make that
// false.
func contentHash(d ReleaseDelta) string {
	raw, err := json.Marshal(d.snapshot())
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// MarshalJSON writes the frozen evidence, snapshot ID and capture time
// included.
func (d ReleaseDelta) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		SnapshotID string    `json:"snapshot_id"`
		CapturedAt time.Time `json:"captured_at"`
		snapshot
	}{
		SnapshotID: d.snapshotID,
		CapturedAt: d.capturedAt,
		snapshot:   d.snapshot(),
	})
}
