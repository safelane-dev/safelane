package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/AndrewMaged814/safelane/internal/release"
)

// legacyNames are the files earlier versions of SafeLane wrote. They are only
// ever looked at to explain why nothing is configured. Nothing here reads them,
// migrates them, or removes them.
var legacyNames = []string{"project.yml", "policy.yml", "process.yml", "release-template"}

// Load reads and validates one `safelane.yml`.
//
// Every failure resolves to the same single instruction - register again -
// because there is nothing else a person can usefully do, and because
// SafeLane will not offer to repair or migrate a file it did not write.
func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, notRegistered(path)
		}
		return Config{}, release.Invalid("unreadable_config", "config",
			fmt.Sprintf("could not read %s: %v", path, err),
			"Fix the file's permissions, or register this application again.")
	}
	cfg, err := Parse(raw)
	if err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Parse decodes and validates configuration bytes.
//
// Unknown fields are rejected rather than ignored. Everything an earlier
// version wrote - a schema version, a controller credential path, a default
// branch, a required check name, an image tag pattern, a template path,
// heuristic settings - is an unknown field now, so a stale file fails loudly
// here instead of loading with half its meaning missing.
func Parse(raw []byte) (Config, error) {
	if err := checkDuplicateLanes(raw); err != nil {
		return Config{}, err
	}

	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, release.Malformed("unreadable_config", "config",
			fmt.Sprintf("this is not a SafeLane configuration file: %v", err),
			"Register this application again; SafeLane does not read files written by earlier versions.")
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// notRegistered is the one sentence SafeLane says about missing or outdated
// configuration. It names the old files when they are there, so a person can
// see that their work is still on disk, and it never deletes them.
func notRegistered(path string) error {
	message := "this application is not registered with SafeLane"
	if leftovers := legacyFilesIn(filepath.Dir(path)); len(leftovers) > 0 {
		message = fmt.Sprintf("this application was configured by an earlier version of SafeLane (%s); that configuration is not read and has been left alone",
			strings.Join(leftovers, ", "))
	}
	return release.Invalid("unregistered_application", "config", message,
		"Register this application again.")
}

func legacyFilesIn(dir string) []string {
	var found []string
	for _, name := range legacyNames {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			found = append(found, name)
		}
	}
	return found
}

// checkDuplicateLanes reports a lane declared twice.
//
// The struct holds lanes in a map, and a map cannot hold the same key twice, so
// by the time the document is decoded one of the two has already won silently.
// Reading the mapping keys as nodes is the only place the duplicate is still
// visible - and a lane silently replaced by a later one with different weights
// is exactly the kind of thing a person needs told.
func checkDuplicateLanes(raw []byte) error {
	lanes := findMapping(raw, "policy", "lanes")
	if lanes == nil {
		return nil
	}
	seen := make(map[string]bool, len(lanes.Content)/2)
	var duplicates []string
	for i := 0; i+1 < len(lanes.Content); i += 2 {
		name := lanes.Content[i].Value
		if seen[name] {
			duplicates = append(duplicates, name)
			continue
		}
		seen[name] = true
	}
	if len(duplicates) == 0 {
		return nil
	}
	sort.Strings(duplicates)
	var errs release.Errors
	for _, name := range duplicates {
		errs = append(errs, release.Invalid("duplicate_lane", "policy.lanes."+name,
			fmt.Sprintf("lane %q is declared twice", name),
			"Give each lane a distinct name, or remove the duplicate declaration."))
	}
	return errs.OrNil()
}

// findMapping walks a document's top-level mapping down the given key path and
// returns the mapping node it lands on, or nil.
func findMapping(raw []byte, path ...string) *yaml.Node {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	if len(doc.Content) == 0 {
		return nil
	}
	node := doc.Content[0]
	for _, key := range path {
		if node.Kind != yaml.MappingNode {
			return nil
		}
		var next *yaml.Node
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == key {
				next = node.Content[i+1]
			}
		}
		if next == nil {
			return nil
		}
		node = next
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	return node
}
