package testgit

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func RealGit() (string, error) {
	locations := gitLocations()
	for _, p := range locations {
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() &&
			(runtime.GOOS == "windows" || fi.Mode()&0111 != 0) {
			return p, nil
		}
	}
	return "", fmt.Errorf("testgit: real git not found in standard locations (%s)", strings.Join(locations, ", "))
}

func gitLocations() []string {
	if runtime.GOOS != "windows" {
		return []string{"/usr/bin/git", "/opt/homebrew/bin/git", "/usr/local/bin/git"}
	}

	locations := make([]string, 0, 8)
	seen := make(map[string]struct{}, 8)
	appendRoot := func(root string) {
		if root == "" || !filepath.IsAbs(root) {
			return
		}
		for _, p := range []string{
			filepath.Join(root, "Git", "cmd", "git.exe"),
			filepath.Join(root, "Git", "bin", "git.exe"),
		} {
			if _, ok := seen[p]; ok {
				continue
			}
			seen[p] = struct{}{}
			locations = append(locations, p)
		}
	}

	appendRoot(os.Getenv("ProgramFiles"))
	appendRoot(`C:\Program Files`)
	appendRoot(os.Getenv("ProgramFiles(x86)"))
	appendRoot(`C:\Program Files (x86)`)
	return locations
}
