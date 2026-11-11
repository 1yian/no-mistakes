package eval

import (
	"path/filepath"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/scm"
)

func RepoDisplayNames(repos []*db.Repo) map[string]string {
	names := map[string]string{}
	for _, repo := range repos {
		if repo == nil {
			continue
		}
		// A repository with no upstream URL was never the source of a case:
		// every fingerprint comes from one. Skipping it also keeps repositories
		// from colliding on the fingerprint of the empty string.
		if strings.TrimSpace(repo.UpstreamURL) == "" {
			continue
		}
		name := RepoDisplayName(repo)
		if name == "" {
			continue
		}
		names[fingerprint(repo.UpstreamURL)] = name
	}
	return names
}

func RepoDisplayName(repo *db.Repo) string {
	if repo == nil {
		return ""
	}
	if path := scm.RepoPath(repo.UpstreamURL); strings.Contains(path, "/") {
		return path
	}
	if path := strings.TrimSpace(repo.WorkingPath); path != "" {
		base := filepath.Base(path)
		if base != "." && base != string(filepath.Separator) && base != "" {
			return base
		}
		return path
	}
	return strings.TrimSpace(repo.ID)
}
