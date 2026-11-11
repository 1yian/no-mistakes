package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEvidenceRootResolvesUnderAppRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("NM_HOME", root)

	p, err := New()
	if err != nil {
		t.Fatal(err)
	}

	if got, want := p.EvidenceDir(), filepath.Join(root, "evidence"); got != want {
		t.Errorf("EvidenceDir() = %q, want %q", got, want)
	}
	if got, want := p.EvidenceRoot(""), filepath.Join(root, "evidence"); got != want {
		t.Errorf("EvidenceRoot(\"\") = %q, want %q", got, want)
	}
	if got, want := p.RunEvidenceDir("", "run-1"), filepath.Join(root, "evidence", "run-1"); got != want {
		t.Errorf("RunEvidenceDir() = %q, want %q", got, want)
	}
}

func TestEvidenceRootIsNeverTheLegacySharedTempDirectory(t *testing.T) {
	legacy := filepath.Join(os.TempDir(), "no-mistakes-evidence")

	for _, root := range []string{t.TempDir(), filepath.Join(t.TempDir(), "nested")} {
		p := WithRoot(root)
		for _, got := range []string{p.EvidenceDir(), p.EvidenceRoot(""), p.RunEvidenceDir("", "run-1")} {
			if got == legacy || strings.HasPrefix(got, legacy+string(filepath.Separator)) {
				t.Fatalf("evidence path %q resolves into the legacy shared temp directory %q", got, legacy)
			}
			if !strings.HasPrefix(got, root+string(filepath.Separator)) {
				t.Fatalf("evidence path %q is not under the app root %q", got, root)
			}
		}
	}
}

func TestEvidenceRootHonorsAbsoluteOverrideAndIgnoresRelative(t *testing.T) {
	root := t.TempDir()
	p := WithRoot(root)
	other := t.TempDir()

	if got := p.EvidenceRoot(other); got != filepath.Clean(other) {
		t.Errorf("EvidenceRoot(%q) = %q, want the configured override", other, got)
	}
	if got, want := p.RunEvidenceDir(other, "run-9"), filepath.Join(other, "run-9"); got != want {
		t.Errorf("RunEvidenceDir(%q) = %q, want %q", other, got, want)
	}

	for _, relative := range []string{"evidence", "./evidence", filepath.Join("..", "evidence")} {
		if got, want := p.EvidenceRoot(relative), p.EvidenceDir(); got != want {
			t.Errorf("EvidenceRoot(%q) = %q, want the app-root default %q", relative, got, want)
		}
	}
	if got, want := p.EvidenceRoot("   "), p.EvidenceDir(); got != want {
		t.Errorf("EvidenceRoot(blank) = %q, want the app-root default %q", got, want)
	}
}

func TestValidateEvidenceRootRefusesWorktreeOverlap(t *testing.T) {
	p := WithRoot(t.TempDir())
	if err := p.ValidateEvidenceRoot(p.WorktreeDir("repo-1", "run-1")); err == nil {
		t.Fatal("expected a worktree-overlapping evidence root to be refused")
	}
}

func TestEnsureDirsDoesNotCreateEvidence(t *testing.T) {
	p := WithRoot(filepath.Join(t.TempDir(), "nm"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.EvidenceDir()); !os.IsNotExist(err) {
		t.Fatalf("EnsureDirs created the evidence directory (stat err = %v)", err)
	}
}
