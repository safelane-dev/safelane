package main

import (
	"bytes"
	"strings"
	"testing"
)

// The command surface is the contract an agent and a person both drive, so it
// is asserted rather than described. Every command decision 11 names is here,
// and everything the clean break removed is an ordinary usage error.

func TestHelpListsTheWholeCommandSurface(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	for _, command := range []string{
		"discover", "register", "inspect", "recommend", "run",
		"status", "hold", "continue", "stop", "proof",
	} {
		if !strings.Contains(stdout.String(), command) {
			t.Errorf("help does not list %q:\n%s", command, stdout.String())
		}
	}
}

// Everything decision 13 removed. A command that still answered would be a
// second way to do the same thing, and the whole point of a clean break is
// that there is one.
func TestTheRemovedSurfaceIsGone(t *testing.T) {
	for _, args := range [][]string{
		{"setup"},
		{"setup", "inspect"},
		{"setup", "plan", "--findings", "findings.json"},
		{"setup", "apply", "setup_01"},
		{"doctor"},
		{"init"},
		{"demo", "up"},
		{"demo", "down"},
		{"release", "plan", "--pr", "1"},
		{"release", "run", "rel_01"},
		{"release", "status"},
		{"release", "proof", "rel_01"},
		{"release", "retry", "rel_01"},
		{"release", "accept-risk", "rel_01"},
		{"release", "pause", "rel_01"},
		{"release", "resume", "rel_01"},
		{"release", "abort", "rel_01"},
		{"rollout", "start"},
		{"rollout", "advance"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Errorf("%v exit = %d, want 2 (an unknown command)", args, code)
		}
	}
}

// No command takes a release identifier, and none of them ever asks for one.
func TestNoCommandAcceptsAReleaseIdentifier(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	for _, forbidden := range []string{"--release", "<release-id>", "rel_", "--yes", "--reason "} {
		if strings.Contains(stdout.String(), forbidden) {
			t.Errorf("the command surface still offers %q:\n%s", forbidden, stdout.String())
		}
	}
}

// --app exists to disambiguate and nothing else, so it takes a value.
func TestAppRequiresAValue(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--app"}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--app requires a value") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestVersionPrints(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) == "" {
		t.Error("version printed nothing")
	}
}
