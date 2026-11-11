package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var InstallBases = []string{
	filepath.Join(".claude", "skills"),
	filepath.Join(".agents", "skills"),
}

func InstallUser() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}
	return Install(home)
}

func Install(root string) ([]string, error) {
	content := []byte(Markdown())
	written := make([]string, 0, len(InstallBases))
	for _, base := range InstallBases {
		rel := filepath.Join(base, Name, "SKILL.md")
		path := filepath.Join(root, rel)
		// Resolve any symlink components to a real directory before creating
		// it, so a dangling symlink in the path does not collide with MkdirAll.
		realDir, err := resolveThroughSymlinks(filepath.Dir(path))
		if err != nil {
			return written, err
		}
		if err := os.MkdirAll(realDir, 0o755); err != nil {
			return written, err
		}
		if err := os.WriteFile(filepath.Join(realDir, "SKILL.md"), content, 0o644); err != nil {
			return written, err
		}
		written = append(written, rel)
	}
	return written, nil
}

func Vendored(repoRoot string) []string {
	var found []string
	for _, base := range InstallBases {
		rel := filepath.Join(base, Name, "SKILL.md")
		if _, err := os.Stat(filepath.Join(repoRoot, rel)); err == nil {
			found = append(found, rel)
		}
	}
	return found
}

func resolveThroughSymlinks(dir string) (string, error) {
	return resolveThroughSymlinksSeen(dir, make(map[string]struct{}))
}

func resolveThroughSymlinksSeen(dir string, seen map[string]struct{}) (string, error) {
	clean := filepath.Clean(dir)
	volume := filepath.VolumeName(clean)
	cur := volume + string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(clean, volume), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err != nil {
			// This component does not exist yet; nothing left to resolve.
			// Remaining parts are appended verbatim onto the resolved prefix.
			continue
		}
		if info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		key := filepath.Clean(cur)
		if _, ok := seen[key]; ok {
			return "", fmt.Errorf("symlink cycle resolving %s", dir)
		}
		seen[key] = struct{}{}
		target, err := os.Readlink(cur)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(cur), target)
		}
		// The target may itself be or contain symlinks; resolve recursively.
		if cur, err = resolveThroughSymlinksSeen(target, seen); err != nil {
			return "", err
		}
	}
	return cur, nil
}
