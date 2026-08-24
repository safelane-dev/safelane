package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// HomeEnv overrides where SafeLane keeps its state. Tests set it; users
// rarely need to.
const HomeEnv = "SAFELANE_HOME"

// FileName is the one configuration file SafeLane reads.
const FileName = "safelane.yml"

// Locations are the Application-level paths. There is only one file here; the
// rest of SafeLane's state is per-Environment.
type Locations struct {
	Home   string
	AppDir string
	// File is `<AppDir>/safelane.yml`.
	File string
}

// EnvLocations are the per-Environment paths. All three are derived from the
// Application and Environment names; none of them appear in YAML.
type EnvLocations struct {
	Dir string
	// ControllerKubeconfig is where the privileged identity's credentials live.
	// It is derived rather than configured on purpose: a path in a settings
	// file is a path somebody can repoint.
	ControllerKubeconfig string
	// ReleasesDir holds the detailed record of each release attempt.
	ReleasesDir string
	// HistoryFile is the compact append-only log behind the history view.
	HistoryFile string
}

// Home returns SAFELANE_HOME, or ~/.safelane when it is unset.
func Home() (string, error) {
	if home := strings.TrimSpace(os.Getenv(HomeEnv)); home != "" {
		return filepath.Abs(home)
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve SafeLane home: %w", err)
	}
	return filepath.Join(userHome, ".safelane"), nil
}

// ForApp derives the Application-level layout from the Application name alone.
func ForApp(home, application string) Locations {
	appDir := filepath.Join(home, "apps", application)
	return Locations{
		Home:   home,
		AppDir: appDir,
		File:   filepath.Join(appDir, FileName),
	}
}

// ForEnvironment derives the per-Environment layout from the Environment name
// alone.
func (l Locations) ForEnvironment(environment string) EnvLocations {
	dir := filepath.Join(l.AppDir, "environments", environment)
	return EnvLocations{
		Dir:                  dir,
		ControllerKubeconfig: filepath.Join(dir, "identities", "controller", "kubeconfig"),
		ReleasesDir:          filepath.Join(dir, "releases"),
		HistoryFile:          filepath.Join(dir, "history.jsonl"),
	}
}

// Apps lists the Application names that have a SafeLane configuration file,
// sorted. A directory without one is not an Application: it is whatever an
// earlier version left behind, and this is how that gets ignored rather than
// half-read.
func Apps(home string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(home, "apps"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read SafeLane applications: %w", err)
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, statErr := os.Stat(ForApp(home, entry.Name()).File); statErr != nil {
			continue
		}
		names = append(names, entry.Name())
	}
	return names, nil
}
