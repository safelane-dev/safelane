package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// pendingInspection keeps the exact candidate chosen by the immediately
// preceding inspect command. Recommend has no revision argument, so this small
// durable handoff prevents it from silently selecting a newer default head.
type pendingInspection struct {
	Snapshot string `json:"snapshot"`
	Revision string `json:"revision"`
}

func inspectionFile(environmentDir string) string {
	return filepath.Join(environmentDir, "inspection.json")
}

func saveInspection(environmentDir string, inspection pendingInspection) error {
	if err := os.MkdirAll(environmentDir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(inspection, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(environmentDir, ".inspection.*.json")
	if err != nil {
		return err
	}
	name := temp.Name()
	cleanup := func() { _ = os.Remove(name) }
	if _, err := temp.Write(append(raw, '\n')); err != nil {
		_ = temp.Close()
		cleanup()
		return err
	}
	if err := temp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		cleanup()
		return err
	}
	return os.Rename(name, inspectionFile(environmentDir))
}

func loadInspection(environmentDir string) (pendingInspection, bool, error) {
	raw, err := os.ReadFile(inspectionFile(environmentDir))
	if os.IsNotExist(err) {
		return pendingInspection{}, false, nil
	}
	if err != nil {
		return pendingInspection{}, false, err
	}
	var inspection pendingInspection
	if err := json.Unmarshal(raw, &inspection); err != nil {
		return pendingInspection{}, false, fmt.Errorf("read pending inspection: %w", err)
	}
	return inspection, true, nil
}
