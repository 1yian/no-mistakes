package worktrees

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/paths"
)

type Layout struct {
	paths *paths.Paths
	roots map[string]string
}

func New(p *paths.Paths, roots map[string]string) *Layout {
	l := &Layout{paths: p}
	if len(roots) == 0 {
		return l
	}
	l.roots = make(map[string]string, len(roots))
	for checkout, root := range roots {
		l.roots[Canonical(checkout)] = filepath.Clean(root)
	}
	return l
}

func (l *Layout) Dir(repoID, workingPath, runID string) string {
	if root, ok := l.CustomRoot(workingPath); ok {
		return filepath.Join(root, runID)
	}
	return l.paths.WorktreeDir(repoID, runID)
}

func RecordedDir(p *paths.Paths, recorded, repoID, runID string) string {
	if trimmed := strings.TrimSpace(recorded); trimmed != "" {
		return filepath.Clean(trimmed)
	}
	return p.WorktreeDir(repoID, runID)
}

func CheckPlacement(p *paths.Paths, checkout, root string, otherCheckouts ...string) error {
	home := p.Root()
	if Contains(home, root) {
		return fmt.Errorf("%q is inside no-mistakes' own state directory (%q), where it would collide with the daemon's worktrees, logs, or gates; choose a directory outside it", root, home)
	}
	if strings.TrimSpace(checkout) != "" && Contains(checkout, root) {
		return fmt.Errorf("%q is inside the checkout whose runs it would hold, which leaves that checkout with an untracked run worktree while a run executes and blocks branch synchronization; choose a directory outside the checkout", root)
	}
	own := Canonical(checkout)
	others := append([]string(nil), otherCheckouts...)
	sort.Strings(others)
	for _, other := range others {
		if strings.TrimSpace(other) == "" || Canonical(other) == own {
			continue
		}
		if Contains(other, root) {
			return fmt.Errorf("%q is inside the checkout %q, which every run placed there would leave with an untracked run worktree, blocking that checkout's branch synchronization while the run executes; choose a directory outside every checkout", root, other)
		}
	}
	return nil
}

func (l *Layout) Validate(known ...string) error {
	checkouts := l.Checkouts()
	sort.Strings(checkouts)
	for _, checkout := range checkouts {
		if err := l.validateEntry(checkout, known); err != nil {
			return err
		}
	}
	return nil
}

func (l *Layout) ValidateCheckout(workingPath string, known ...string) error {
	if _, ok := l.CustomRoot(workingPath); !ok {
		return nil
	}
	return l.validateEntry(Canonical(workingPath), known)
}

func (l *Layout) validateEntry(checkout string, known []string) error {
	root, ok := l.roots[checkout]
	if !ok {
		return nil
	}
	checkouts := l.Checkouts()
	protected := make([]string, 0, len(known)+len(checkouts))
	protected = append(protected, known...)
	protected = append(protected, checkouts...)
	if err := CheckPlacement(l.paths, checkout, root, protected...); err != nil {
		return fmt.Errorf("invalid worktree_roots[%q]: run worktree root %w", checkout, err)
	}
	return nil
}

func (l *Layout) CustomRoot(workingPath string) (string, bool) {
	if len(l.roots) == 0 || strings.TrimSpace(workingPath) == "" {
		return "", false
	}
	root, ok := l.roots[Canonical(workingPath)]
	return root, ok
}

func (l *Layout) Checkouts() []string {
	out := make([]string, 0, len(l.roots))
	for checkout := range l.roots {
		out = append(out, checkout)
	}
	return out
}

func Canonical(path string) string {
	cleaned := filepath.Clean(path)
	if abs, err := filepath.Abs(cleaned); err == nil {
		cleaned = abs
	}
	// EvalSymlinks fails outright on a path that does not exist yet, and a
	// configured worktree root usually does not until its first run. Resolve
	// the deepest existing ancestor and keep the remainder, so a root and the
	// checkout it belongs to are still compared in the same spelling.
	current, rest := cleaned, ""
	for {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			return filepath.Clean(filepath.Join(resolved, rest))
		}
		parent := filepath.Dir(current)
		if parent == current {
			return cleaned
		}
		rest = filepath.Join(filepath.Base(current), rest)
		current = parent
	}
}

func Contains(dir, path string) bool {
	return containsBySpelling(dir, path) || containsByIdentity(dir, path)
}

func containsBySpelling(dir, path string) bool {
	rel, err := filepath.Rel(Canonical(dir), Canonical(path))
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel))
}

func containsByIdentity(dir, path string) bool {
	dirInfo, err := os.Stat(dir)
	if err != nil {
		return false
	}
	current := Canonical(path)
	for {
		if info, err := os.Stat(current); err == nil && os.SameFile(dirInfo, info) {
			return true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false
		}
		current = parent
	}
}
