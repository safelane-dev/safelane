package releasepatch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/AndrewMaged814/safelane/internal/release"
)

// Pending is the one recommendation awaiting approval for an Application and
// Environment.
//
// There is at most one, which is why nothing addresses it by identifier: the
// Application and the Environment are the address. A second pending
// recommendation would mean a person could approve one and run the other.
type Pending struct {
	Application string `json:"application"`
	Environment string `json:"environment"`
	Snapshot    string `json:"snapshot"`
	Revision    string `json:"revision"`
	// Action is the recommendation's action. A waiting one is stored so
	// `run` can say what it is waiting on rather than "there is nothing here".
	Action string `json:"action"`
	Lane   string `json:"lane,omitempty"`
	Patch  Patch  `json:"patch"`
	// Delta and Recommendation are the frozen proof inputs. They are stored
	// once here and copied into the durable Release Attempt when approval is
	// spent; the conversation and abandoned drafts are never retained.
	Delta          json.RawMessage `json:"delta,omitempty"`
	Recommendation json.RawMessage `json:"recommendation,omitempty"`
	// Facts are what was true when the recommendation was frozen. The recheck
	// compares against these.
	Facts    Facts     `json:"facts"`
	Analysis []string  `json:"analysis,omitempty"`
	At       time.Time `json:"at"`
	// Attempts is how many assessments have been submitted for this snapshot.
	// It lives here so a session cannot restart the count by resubmitting.
	Attempts int `json:"attempts"`
	// Approval is set once a person has said yes.
	Approval *Approval `json:"approval,omitempty"`
}

// PendingFile is where the pending recommendation lives for one Environment.
func PendingFile(environmentDir string) string {
	return filepath.Join(environmentDir, "pending.json")
}

// SavePending writes the pending recommendation atomically.
func SavePending(environmentDir string, pending Pending) error {
	if err := os.MkdirAll(environmentDir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", environmentDir, err)
	}
	raw, err := json.MarshalIndent(pending, "", "  ")
	if err != nil {
		return err
	}
	path := PendingFile(environmentDir)
	temp, err := os.CreateTemp(environmentDir, ".pending.*.json")
	if err != nil {
		return fmt.Errorf("create a temporary file in %s: %w", environmentDir, err)
	}
	name := temp.Name()
	if _, err := temp.Write(append(raw, '\n')); err != nil {
		temp.Close()
		os.Remove(name)
		return err
	}
	if err := temp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}

// LoadPending reads the pending recommendation.
//
// Nothing there is not an error condition to be papered over: it is the answer
// to "is there something to run", and the answer is no.
func LoadPending(environmentDir string) (Pending, bool, error) {
	raw, err := os.ReadFile(PendingFile(environmentDir))
	if os.IsNotExist(err) {
		return Pending{}, false, nil
	}
	if err != nil {
		return Pending{}, false, fmt.Errorf("read %s: %w", PendingFile(environmentDir), err)
	}
	var pending Pending
	if err := json.Unmarshal(raw, &pending); err != nil {
		return Pending{}, false, release.Invalid("unreadable_pending_release", "release",
			fmt.Sprintf("could not read the pending recommendation: %v", err),
			"Ask me to look at this release again.")
	}
	return pending, true, nil
}

// ClearPending removes the pending recommendation once it has been spent.
func ClearPending(environmentDir string) error {
	err := os.Remove(PendingFile(environmentDir))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// NothingAwaitingApproval is what `run` says when there is no recommendation.
//
// It does not offer to make one. Assessing and approving are separate acts on
// purpose, and a `run` that quietly assessed first would collapse them - the
// person would be approving something they had not read.
func NothingAwaitingApproval(application, environment string) error {
	return release.Invalid("nothing_awaiting_approval", "release",
		fmt.Sprintf("there is no recommendation waiting for approval for %s in %s", application, environment),
		fmt.Sprintf("Ask me to look at releasing %s to %s first.", application, environment))
}

// WaitingCannotRun is what `run` says when the pending recommendation is to
// wait.
func WaitingCannotRun(application, environment string) error {
	return release.Invalid("cannot_approve_a_waiting_recommendation", "release",
		fmt.Sprintf("the current recommendation for %s in %s is to wait", application, environment),
		"Resolve what it is waiting on, and I will reassess.")
}
