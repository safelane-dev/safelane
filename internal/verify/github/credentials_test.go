package github

import (
	"context"
	"errors"
	"testing"
)

func TestResolveTokenUsesGitHubTokenFirst(t *testing.T) {
	called := false
	token := resolveToken(context.Background(), func(name string) string {
		if name == "GITHUB_TOKEN" {
			return "github-token"
		}
		return "gh-token"
	}, func(context.Context) ([]byte, error) {
		called = true
		return []byte("cli-token"), nil
	})
	if token != "github-token" || called {
		t.Fatalf("token = %q, gh called = %v", token, called)
	}
}

func TestResolveTokenUsesGHTokenBeforeGitHubCLI(t *testing.T) {
	called := false
	token := resolveToken(context.Background(), func(name string) string {
		if name == "GH_TOKEN" {
			return "gh-token"
		}
		return ""
	}, func(context.Context) ([]byte, error) {
		called = true
		return []byte("cli-token"), nil
	})
	if token != "gh-token" || called {
		t.Fatalf("token = %q, gh called = %v", token, called)
	}
}

func TestResolveTokenUsesExistingGitHubCLILogin(t *testing.T) {
	token := resolveToken(context.Background(), func(string) string { return "" },
		func(context.Context) ([]byte, error) { return []byte("  cli-token\r\n"), nil })
	if token != "cli-token" {
		t.Fatalf("token = %q", token)
	}
}

func TestResolveTokenLeavesPublicAccessAvailableWithoutCredentials(t *testing.T) {
	token := resolveToken(context.Background(), func(string) string { return "" },
		func(context.Context) ([]byte, error) { return nil, errors.New("gh is not installed") })
	if token != "" {
		t.Fatalf("token = %q", token)
	}
}
