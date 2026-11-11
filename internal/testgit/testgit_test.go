package testgit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRealGit_IgnoresWrapperOnPATH(t *testing.T) {
	if _, err := os.Stat("/usr/bin/git"); err != nil {
		t.Skip("/usr/bin/git not present on this host")
	}

	wrapperDir := t.TempDir()
	wrapper := filepath.Join(wrapperDir, "git")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	got, err := RealGit()
	if err != nil {
		t.Fatalf("RealGit() error = %v", err)
	}
	if got == wrapper {
		t.Fatalf("RealGit() returned the PATH-shadowing wrapper %q, want an absolute real-git path", got)
	}
	if got != "/usr/bin/git" {
		t.Fatalf("RealGit() = %q, want /usr/bin/git", got)
	}
}
