package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/paths"
)

func swapCase(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r - 'a' + 'A'
		case r >= 'A' && r <= 'Z':
			return r - 'A' + 'a'
		}
		return r
	}, s)
}

func filesystemFoldsCase(t *testing.T, dir string) bool {
	t.Helper()
	probe := filepath.Join(dir, "CaseProbe")
	if err := os.Mkdir(probe, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := os.Stat(filepath.Join(dir, "caseprobe"))
	return err == nil
}

func TestOwnedGateAcceptsACaseVariantSpellingOnACaseInsensitiveFilesystem(t *testing.T) {
	base := t.TempDir()
	if !filesystemFoldsCase(t, base) {
		t.Skip("filesystem is case-sensitive: a case variant is a different directory here")
	}

	const repoID = "case-variant-repo"
	p := paths.WithRoot(filepath.Join(base, "NmHome"))
	if err := os.MkdirAll(p.RepoDir(repoID), 0o755); err != nil {
		t.Fatal(err)
	}
	gate := filepath.Join(base, "nmhome", "repos", repoID+".git")

	got, err := ownedGateRepoID(p, gate)
	if err != nil {
		t.Fatalf("a case-variant spelling of the daemon's own gate must be owned: %v", err)
	}
	if got != repoID {
		t.Fatalf("repo id = %q, want %q", got, repoID)
	}
}

func TestOwnedGateRefusesACaseVariantThatIsAnotherDirectory(t *testing.T) {
	base := t.TempDir()
	if filesystemFoldsCase(t, base) {
		t.Skip("filesystem is case-insensitive: a case variant is the same directory here")
	}

	const repoID = "case-sensitive-repo"
	p := paths.WithRoot(filepath.Join(base, "NmHome"))
	if err := os.MkdirAll(p.RepoDir(repoID), 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(base, swapCase("NmHome"), "repos", repoID+".git")
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := ownedGateRepoID(p, foreign); err == nil {
		t.Fatal("a differently-cased gate that is a distinct directory must be refused")
	}
}

func TestOwnedGateRefusesAMissingCaseVariant(t *testing.T) {
	base := t.TempDir()
	const repoID = "missing-variant-repo"
	p := paths.WithRoot(filepath.Join(base, "NmHome"))
	if err := os.MkdirAll(p.RepoDir(repoID), 0o755); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(base, "OtherHome", "repos", repoID+".git")

	if _, err := ownedGateRepoID(p, other); err == nil {
		t.Fatal("a gate under a different, non-existent root must be refused")
	}
}
