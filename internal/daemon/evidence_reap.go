package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/worktrees"
)

const legacyEvidenceDirName = "no-mistakes-evidence"

type evidenceReapPolicy struct {
	Retention time.Duration
	MaxRuns   int
}

func evidenceReapPolicyFor(global *config.GlobalConfig) evidenceReapPolicy {
	policy := evidenceReapPolicy{
		Retention: config.DefaultEvidenceRetention,
		MaxRuns:   config.DefaultEvidenceMaxRuns,
	}
	if global == nil {
		return policy
	}
	resolved := config.Merge(global, &config.RepoConfig{})
	policy.Retention = resolved.Test.Evidence.Retention
	policy.MaxRuns = resolved.Test.Evidence.MaxRuns
	return policy
}

func evidenceRootFor(p *paths.Paths, global *config.GlobalConfig) string {
	roots := evidenceRootsFor(p, global, nil)
	if len(roots) == 0 {
		return ""
	}
	return roots[0]
}

func evidenceWorktreePaths(p *paths.Paths, global *config.GlobalConfig, d *db.DB) ([]string, error) {
	dirs := []string{p.WorktreesDir()}
	if global != nil {
		for checkout, root := range global.WorktreeRoots {
			dirs = append(dirs, checkout, root)
		}
	}
	if d != nil {
		recorded, err := d.GetRunWorktreeDirs()
		if err != nil {
			return nil, err
		}
		dirs = append(dirs, recorded...)
		repos, err := d.GetRepos()
		if err != nil {
			return nil, err
		}
		for _, repo := range repos {
			dirs = append(dirs, repo.WorkingPath)
		}
	}
	return dirs, nil
}

func evidencePlacementConflict(root string, worktreePaths []string) string {
	for _, dir := range worktreePaths {
		if dir != "" && (worktrees.Contains(dir, root) || worktrees.Contains(root, dir)) {
			return dir
		}
	}
	return ""
}

func evidenceRootsFor(p *paths.Paths, global *config.GlobalConfig, d *db.DB) []string {
	if p == nil {
		return nil
	}
	evidenceDir := ""
	localRoot := ""
	if global != nil {
		evidenceDir = global.EvidenceDir
		localRoot = config.Merge(global, &config.RepoConfig{}).Test.Evidence.LocalRoot
	}
	root, warning := p.ResolveEvidenceRoot(evidenceDir, localRoot)
	if warning != "" {
		slog.Warn("evidence directory fallback", "detail", warning)
	}
	worktreePaths, err := evidenceWorktreePaths(p, global, d)
	if err != nil {
		slog.Warn("failed to discover worktree placements for evidence cleanup", "error", err)
		return nil
	}
	seen := map[string]bool{}
	var roots []string
	add := func(candidate string) {
		candidate = filepath.Clean(candidate)
		if candidate == "" || candidate == "." || seen[candidate] || evidencePlacementConflict(candidate, worktreePaths) != "" {
			return
		}
		seen[candidate] = true
		roots = append(roots, candidate)
	}
	add(root)
	add(p.EvidenceDir())
	add(p.EvidenceRoot(localRoot))
	if d != nil {
		dirs, err := d.GetRunEvidenceDirs()
		if err != nil {
			slog.Warn("failed to discover pinned evidence roots", "error", err)
		}
		for _, dir := range dirs {
			add(filepath.Dir(dir))
		}
	}
	return roots
}

func (m *RunManager) pinRunEvidenceDir(run *db.Run, global *config.GlobalConfig) error {
	if m == nil || run == nil || global == nil || run.EvidencePath() != "" {
		return nil
	}
	root, warning := m.paths.ResolveEvidenceRoot(global.EvidenceDir, config.Merge(global, &config.RepoConfig{}).Test.Evidence.LocalRoot)
	worktreePaths, err := evidenceWorktreePaths(m.paths, global, m.db)
	if err != nil {
		return fmt.Errorf("read worktree placements: %w", err)
	}
	if conflict := evidencePlacementConflict(root, worktreePaths); conflict != "" {
		warning = fmt.Sprintf("evidence directory %q overlaps worktree placement %q; using %s", root, conflict, m.paths.EvidenceDir())
		root = m.paths.EvidenceDir()
	}
	if warning != "" {
		slog.Warn("evidence directory fallback", "run_id", run.ID, "detail", warning)
	}
	if conflict := evidencePlacementConflict(root, worktreePaths); conflict != "" {
		return fmt.Errorf("default evidence directory %q overlaps worktree placement %q", root, conflict)
	}
	dir := filepath.Join(root, run.ID)
	if err := m.db.SetRunEvidenceDir(run.ID, dir); err != nil {
		return err
	}
	run.EvidenceDir = &dir
	return nil
}

func (m *RunManager) recordedRunEvidenceDir(cfg *config.Config, runID string) string {
	if m != nil && m.db != nil && runID != "" {
		if run, err := m.db.GetRun(runID); err == nil && run != nil && run.EvidencePath() != "" {
			return run.EvidencePath()
		}
	}
	configured := ""
	if cfg != nil {
		configured = cfg.Test.Evidence.LocalRoot
	}
	if m == nil || m.paths == nil {
		return ""
	}
	return m.paths.RunEvidenceDir(configured, runID)
}

func pipelineEvidenceRoot(p *paths.Paths, cfg *config.Config, run *db.Run) string {
	if run != nil && run.EvidencePath() != "" {
		return run.EvidencePath()
	}
	if p == nil {
		return ""
	}
	configured := ""
	if cfg != nil {
		configured = cfg.Test.Evidence.LocalRoot
	}
	return p.EvidenceRoot(configured)
}

func reapEvidence(d *db.DB, root string, policy evidenceReapPolicy, now time.Time, additionalRoots ...string) {
	type candidate struct {
		path    string
		modTime time.Time
		isEmpty bool
		runID   string
	}
	var candidates []candidate
	seen := map[string]bool{}
	for _, root := range append([]string{root}, additionalRoots...) {
		root = worktrees.Canonical(root)
		if seen[root] {
			continue
		}
		seen[root] = true
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			runID := entry.Name()
			run, err := d.GetRun(runID)
			if err != nil {
				slog.Debug("skipping evidence cleanup", "run_id", runID, "reason", err)
				continue
			}
			if run == nil {
				continue
			}
			if skip, reason := skipWorktreeCleanup(context.Background(), d, runID, run.WorktreePath()); skip {
				slog.Debug("skipping evidence cleanup", "run_id", runID, "reason", reason)
				continue
			}
			path := filepath.Join(root, runID)
			if pinned := run.EvidencePath(); pinned != "" && worktrees.Canonical(pinned) != worktrees.Canonical(path) {
				continue
			}
			if wt := run.WorktreePath(); wt != "" && evidencePlacementConflict(path, []string{wt}) != "" {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			inner, err := os.ReadDir(path)
			if err != nil {
				continue
			}
			candidates = append(candidates, candidate{
				path:    path,
				modTime: info.ModTime(),
				isEmpty: len(inner) == 0,
				runID:   runID,
			})
		}

	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].modTime.Before(candidates[j].modTime)
	})

	removed := 0
	survivors := make([]candidate, 0, len(candidates))
	for _, c := range candidates {
		expired := policy.Retention > 0 && now.Sub(c.modTime) > policy.Retention
		if c.isEmpty || expired {
			if removeEvidenceDir(c.path, c.runID) {
				removed++
			}
			continue
		}
		survivors = append(survivors, c)
	}

	if policy.MaxRuns > 0 && len(survivors) > policy.MaxRuns {
		for _, c := range survivors[:len(survivors)-policy.MaxRuns] {
			if removeEvidenceDir(c.path, c.runID) {
				removed++
			}
		}
	}

	if removed > 0 {
		slog.Info("reaped run evidence", "roots", len(seen), "removed", removed)
	}
}

func removeEvidenceDir(path, runID string) bool {
	if err := os.RemoveAll(path); err != nil {
		slog.Warn("failed to remove run evidence", "path", path, "run_id", runID, "error", err)
		return false
	}
	return true
}

func reapLegacyEvidence(d *db.DB, current string, policy evidenceReapPolicy, now time.Time, additionalRoots ...string) {
	legacy := filepath.Join(os.TempDir(), legacyEvidenceDirName)
	roots := append([]string{current}, additionalRoots...)
	roots = append(roots, legacy)
	reapEvidence(d, roots[0], policy, now, roots[1:]...)
	// Remove the root itself once it is empty; os.Remove fails harmlessly while
	// anything remains.
	if !worktrees.Contains(current, legacy) {
		_ = os.Remove(legacy)
	}
}
