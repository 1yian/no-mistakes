package branchsync

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

var gitFixtureRoot string

var gitFixtures sync.Map

type gitFixture[T any] struct {
	once  sync.Once
	dir   string
	heads T
	built bool
}

func copyGitFixture[T any](t *testing.T, key string, build func(t *testing.T, root string) T) (string, T) {
	t.Helper()
	cached, _ := gitFixtures.LoadOrStore(key, &gitFixture[T]{})
	fixture := cached.(*gitFixture[T])
	fixture.once.Do(func() {
		fixture.dir = filepath.Join(gitFixtureRoot, key)
		if err := os.MkdirAll(fixture.dir, 0o755); err != nil {
			t.Fatal(err)
		}
		fixture.heads = build(t, fixture.dir)
		fixture.built = true
	})
	if !fixture.built {
		t.Fatalf("git fixture %s failed to build", key)
	}
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS(fixture.dir)); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		dir := filepath.Join(root, entry.Name())
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			// A copy changes every file's stat data; refresh the index now
			// so the first status a test runs does not rewrite it.
			mustRun(t, dir, "update-index", "-q", "--refresh")
		}
	}
	return root, fixture.heads
}

func removeScratchClone(t *testing.T, dir string) {
	t.Helper()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
}
