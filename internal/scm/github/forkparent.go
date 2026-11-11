package github

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

func RepoParent(ctx context.Context, cmd CmdFactory, host, remoteURL string) (parentSlug string, isFork bool, err error) {
	slug := RepoSlug(remoteURL)
	if slug == "" {
		return "", false, fmt.Errorf("resolve GitHub repository for %q", remoteURL)
	}
	args := []string{"api"}
	if host != "" {
		args = append(args, "--hostname", host)
	}
	args = append(args, "repos/"+slug)
	out, err := cmd(ctx, "gh", args...).Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", false, fmt.Errorf("gh api repos/%s: %s: %w", slug, strings.TrimSpace(string(exitErr.Stderr)), err)
		}
		return "", false, fmt.Errorf("gh api repos/%s: %w", slug, err)
	}
	var payload struct {
		Fork   bool `json:"fork"`
		Parent struct {
			FullName string `json:"full_name"`
		} `json:"parent"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return "", false, fmt.Errorf("parse gh api repos/%s response: %w", slug, err)
	}
	return strings.TrimSpace(payload.Parent.FullName), payload.Fork, nil
}
