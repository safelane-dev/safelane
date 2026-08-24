// Package skill carries the agent skill SafeLane installs.
//
// It is one embedded source so every installed copy is byte-identical: a skill
// that drifted per harness would mean two agents driving the same tool from
// different instructions.
package skill

import _ "embed"

// SafeLane is the canonical skill artifact installed for every supported agent
// harness. Keeping one embedded source makes installed copies byte-identical.
//
//go:embed SKILL.md
var SafeLane []byte
