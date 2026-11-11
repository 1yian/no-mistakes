package worktrees_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/worktrees"
)

func namesPath(err error, path string) bool {
	msg := err.Error()
	for _, spelling := range []string{path, worktrees.Canonical(path)} {
		if strings.Contains(msg, spelling) || strings.Contains(msg, strconv.Quote(spelling)) {
			return true
		}
	}
	return false
}

func TestLayout_DefaultPlacement(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	checkout := filepath.Join(t.TempDir(), "src", "repo1")
	layout := worktrees.New(p, nil)

	if got, want := layout.Dir("repo1", checkout, "run1"), p.WorktreeDir("repo1", "run1"); got != want {
		t.Errorf("Dir() = %q, want %q", got, want)
	}
	if root, ok := layout.CustomRoot(checkout); ok {
		t.Errorf("CustomRoot() = %q, true; want no configured root", root)
	}
}

func TestLayout_ConfiguredPlacement(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	dir := t.TempDir()
	checkout := filepath.Join(dir, "src", "repo1")
	configured := filepath.Join(dir, "work", "repo1-runs")
	layout := worktrees.New(p, map[string]string{checkout: configured})

	if got, want := layout.Dir("repo1", checkout, "run1"), filepath.Join(configured, "run1"); got != want {
		t.Errorf("Dir() = %q, want %q", got, want)
	}
	other := filepath.Join(dir, "src", "repo2")
	if got, want := layout.Dir("repo2", other, "run2"), p.WorktreeDir("repo2", "run2"); got != want {
		t.Errorf("unconfigured repository Dir() = %q, want %q", got, want)
	}
	root, ok := layout.CustomRoot(checkout)
	if !ok || root != configured {
		t.Errorf("CustomRoot() = %q, %v; want %q, true", root, ok, configured)
	}
	if got := layout.Checkouts(); len(got) != 1 || got[0] != worktrees.Canonical(checkout) {
		t.Errorf("Checkouts() = %v, want [%q]", got, worktrees.Canonical(checkout))
	}
}

func TestLayout_MatchesUnnormalizedAndSymlinkedCheckout(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	dir := t.TempDir()
	checkout := filepath.Join(dir, "src", "repo1")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(checkout, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	configured := filepath.Join(dir, "work", "repo1-runs")
	layout := worktrees.New(p, map[string]string{filepath.Join(checkout, "."): configured})

	for _, spelling := range []string{checkout, link, checkout + string(filepath.Separator)} {
		if got, want := layout.Dir("repo1", spelling, "run1"), filepath.Join(configured, "run1"); got != want {
			t.Errorf("Dir(%q) = %q, want %q", spelling, got, want)
		}
	}
}

func TestRecordedDirReadsBackWhatTheRunRecorded(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	created := filepath.Join(t.TempDir(), "work", "repo1-runs", "run1")

	if got := worktrees.RecordedDir(p, created, "repo1", "run1"); got != created {
		t.Errorf("RecordedDir() = %q, want the recorded %q", got, created)
	}
}

func TestRecordedDirResolvesUnrecordedRunsToTheDefaultTree(t *testing.T) {
	p := paths.WithRoot(t.TempDir())

	for _, recorded := range []string{"", "  "} {
		if got, want := worktrees.RecordedDir(p, recorded, "repo1", "run1"), p.WorktreeDir("repo1", "run1"); got != want {
			t.Errorf("RecordedDir(%q) = %q, want the default placement %q", recorded, got, want)
		}
	}
}

func TestLayout_ValidateRefusesRootInsideAppState(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	checkout := filepath.Join(t.TempDir(), "src", "repo1")

	for name, root := range map[string]string{
		"the worktrees directory itself": p.WorktreesDir(),
		"a directory inside it":          filepath.Join(p.WorktreesDir(), "my-runs"),
		"a per-repository directory":     filepath.Join(p.WorktreesDir(), "repo1"),
		"the run log directory":          p.LogsDir(),
		"the gates directory":            p.ReposDir(),
		"NM_HOME itself":                 p.Root(),
	} {
		err := worktrees.New(p, map[string]string{checkout: root}).Validate()
		if err == nil {
			t.Errorf("%s (%q) was accepted; want a refusal", name, root)
			continue
		}
		if !namesPath(err, root) || !strings.Contains(err.Error(), "worktree_roots") {
			t.Errorf("%s: refusal %q names neither the setting nor the root", name, err)
		}
	}
}

func TestLayout_ValidateRefusesRootInsideItsCheckout(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	checkout := filepath.Join(t.TempDir(), "src", "repo1")

	for _, root := range []string{
		filepath.Join(checkout, "runs"),
		filepath.Join(checkout, ".no-mistakes", "runs"),
	} {
		err := worktrees.New(p, map[string]string{checkout: root}).Validate()
		if err == nil {
			t.Errorf("root %q inside its own checkout was accepted; want a refusal", root)
			continue
		}
		if !namesPath(err, root) || !strings.Contains(err.Error(), "worktree_roots") {
			t.Errorf("refusal %q names neither the setting nor the root", err)
		}
	}
	// A sibling of the checkout is the normal case and must stay accepted.
	sibling := filepath.Join(filepath.Dir(checkout), "repo1-runs")
	if err := worktrees.New(p, map[string]string{checkout: sibling}).Validate(); err != nil {
		t.Errorf("root next to the checkout rejected: %v", err)
	}
}

func TestLayout_ValidateRefusesRootInsideAnotherKnownCheckout(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	dir := t.TempDir()
	own := filepath.Join(dir, "src", "own")
	other := filepath.Join(dir, "src", "other")
	root := filepath.Join(other, "runs")

	// Known only because the other checkout also has an entry.
	both := worktrees.New(p, map[string]string{
		own:   root,
		other: filepath.Join(dir, "work", "other-runs"),
	})
	err := both.Validate()
	if err == nil {
		t.Fatalf("root %q inside the configured checkout %q was accepted; want a refusal", root, other)
	}
	if !namesPath(err, root) || !namesPath(err, other) {
		t.Errorf("refusal %q does not name both the root and the checkout it would dirty", err)
	}

	// Known only because the caller supplied it, which is how a registered
	// repository with no entry of its own reaches this rule.
	configured := worktrees.New(p, map[string]string{own: root})
	if err := configured.Validate(); err != nil {
		t.Fatalf("unknown checkout must not be judged: %v", err)
	}
	if err := configured.Validate(other); err == nil {
		t.Errorf("root %q inside the registered checkout %q was accepted; want a refusal", root, other)
	}
}

func TestLayout_ValidateAcceptsPlacementOutsideAppState(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	dir := t.TempDir()
	checkout := filepath.Join(dir, "src", "repo1")

	if err := worktrees.New(p, nil).Validate(); err != nil {
		t.Errorf("default placement rejected: %v", err)
	}
	layout := worktrees.New(p, map[string]string{checkout: filepath.Join(dir, "work", "repo1-runs")})
	if err := layout.Validate(); err != nil {
		t.Errorf("placement outside NM_HOME rejected: %v", err)
	}
}

func TestContains(t *testing.T) {
	dir := t.TempDir()
	inside := filepath.Join(dir, "runs")
	if !worktrees.Contains(dir, inside) {
		t.Errorf("Contains(%q, %q) = false, want true", dir, inside)
	}
	if !worktrees.Contains(dir, dir) {
		t.Errorf("Contains(%q, %q) = false, want true for the directory itself", dir, dir)
	}
	sibling := filepath.Join(filepath.Dir(dir), filepath.Base(dir)+"-sibling")
	if worktrees.Contains(dir, sibling) {
		t.Errorf("Contains(%q, %q) = true, want false", dir, sibling)
	}
}

func TestContainsFollowsTheFilesystemAcrossCaseVariantSpellings(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "nm-home")
	if err := os.MkdirAll(filepath.Join(home, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	variantHome := filepath.Join(base, strings.ToUpper("nm-home"))
	variantChild := filepath.Join(variantHome, "logs")

	// Whether the two spellings are one directory is the volume's answer, not
	// ours, so the expectation comes from the volume.
	sameDirectory := false
	if a, err := os.Stat(home); err == nil {
		if b, err := os.Stat(variantHome); err == nil {
			sameDirectory = os.SameFile(a, b)
		}
	}

	if got := worktrees.Contains(home, variantChild); got != sameDirectory {
		t.Errorf("Contains(%q, %q) = %v, want %v (the volume reports the two spellings as the same directory: %v)",
			home, variantChild, got, sameDirectory, sameDirectory)
	}
	// A genuinely different sibling stays outside on every volume.
	if worktrees.Contains(home, filepath.Join(base, "nm-home-elsewhere", "logs")) {
		t.Error("a sibling directory was reported as inside NM_HOME")
	}
}

func TestCheckPlacementRefusesCaseVariantAppStateRoot(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "nm-home")
	if err := os.MkdirAll(filepath.Join(home, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	variantRoot := filepath.Join(base, strings.ToUpper("nm-home"), "logs")

	sameDirectory := false
	if a, err := os.Stat(home); err == nil {
		if b, err := os.Stat(filepath.Dir(variantRoot)); err == nil {
			sameDirectory = os.SameFile(a, b)
		}
	}
	if !sameDirectory {
		t.Skip("case-sensitive volume: the variant spelling is a genuinely different directory")
	}

	p := paths.WithRoot(home)
	checkout := filepath.Join(base, "src", "repo1")
	err := worktrees.CheckPlacement(p, checkout, variantRoot)
	if err == nil {
		t.Fatalf("root %q was accepted; it is NM_HOME's own log directory on this volume", variantRoot)
	}
	if !namesPath(err, variantRoot) {
		t.Errorf("refusal %q does not name the offending root", err)
	}
}
