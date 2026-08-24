// Package cli is what every SafeLane command does, minus the argument
// parsing.
//
// Each command is a function taking an options struct and two writers, so the
// whole surface is exercisable from a test with no binary, no terminal, and no
// cluster. The cobra wiring in cmd/safelane only turns argv into those structs.
//
// The shared conventions - machine output when piped, readable text at a
// terminal, and the Application inferred from the checkout's Git origin - live
// in surface.go, once, because a convention each command re-decides is not a
// convention.
package cli

import (
	"fmt"
	"io"
)

// Exit codes follow the Unix convention an agent can branch on without parsing
// output.
//
// ExitDecision is the one worth knowing about: it means SafeLane ran, reached a
// decision, and that decision was not "yes". A rejected assessment awaiting its
// one correction returns it. An agent that treated that as a failure would
// retry the wrong thing.
const (
	ExitOK       = 0
	ExitFail     = 1
	ExitUsage    = 2
	ExitDecision = 4
)

func writeResultError(stderr io.Writer, command string, err error) int {
	fmt.Fprintf(stderr, "safelane %s: %v\n", command, err)
	return ExitFail
}
