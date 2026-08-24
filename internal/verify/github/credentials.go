package github

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

// Token resolves the credentials people already use with GitHub. Explicit
// process credentials win; an authenticated GitHub CLI is the final fallback.
// An empty result keeps public, rate-limited access available.
func Token(ctx context.Context) string {
	return resolveToken(ctx, os.Getenv, func(ctx context.Context) ([]byte, error) {
		return exec.CommandContext(ctx, "gh", "auth", "token").Output()
	})
}

func resolveToken(ctx context.Context, getenv func(string) string,
	githubCLI func(context.Context) ([]byte, error)) string {
	for _, name := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if token := strings.TrimSpace(getenv(name)); token != "" {
			return token
		}
	}
	raw, err := githubCLI(ctx)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}
