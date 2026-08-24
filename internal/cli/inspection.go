package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/AndrewMaged814/safelane/internal/privatefile"
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
	raw, err := json.MarshalIndent(inspection, "", "  ")
	if err != nil {
		return err
	}
	return privatefile.WriteAtomic(inspectionFile(environmentDir), append(raw, '\n'))
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
