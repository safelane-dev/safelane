// Package privatefile owns the one atomic persistence pattern used for
// SafeLane's private state files.
package privatefile

import (
	"os"
	"path/filepath"
)

// WriteAtomic creates parent directories privately, writes through a unique
// temporary file, applies private permissions, and removes the temporary file
// on every failure path before replacing the destination.
func WriteAtomic(path string, content []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".safelane-state.*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	cleanup := func() { _ = os.Remove(name) }
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		cleanup()
		return err
	}
	if err := temporary.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(name, path); err != nil {
		cleanup()
		return err
	}
	return nil
}
