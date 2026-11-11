package daemon

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/db"
)

func reapRunLogs(d *db.DB, root string, policy evidenceReapPolicy, now time.Time) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return // directory may not exist yet, which is the normal case
	}

	type candidate struct {
		path    string
		modTime time.Time
		runID   string
	}
	candidates := make([]candidate, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		runID := entry.Name()
		run, err := d.GetRun(runID)
		if err != nil {
			slog.Debug("skipping run log cleanup", "run_id", runID, "reason", err)
			continue
		}
		if run == nil {
			continue
		}
		if skip, reason := skipWorktreeCleanup(context.Background(), d, runID, run.WorktreePath()); skip {
			slog.Debug("skipping run log cleanup", "run_id", runID, "reason", reason)
			continue
		}
		path := filepath.Join(root, runID)
		info, err := entry.Info()
		if err != nil {
			continue
		}
		// The directory's own mtime only advances when a step log file is
		// created inside it, not when an already-open one is appended to, so
		// a long-running step's most recent writes can leave the directory
		// looking far older than its logs actually are. The newest
		// contained file's mtime catches that recent activity; the
		// directory's own mtime is still the floor for a directory with no
		// files yet.
		modTime := info.ModTime()
		if newest, ok := newestFileModTime(path); ok && newest.After(modTime) {
			modTime = newest
		}
		candidates = append(candidates, candidate{path: path, modTime: modTime, runID: runID})
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].modTime.Before(candidates[j].modTime)
	})

	removed := 0
	survivors := make([]candidate, 0, len(candidates))
	for _, c := range candidates {
		expired := policy.Retention > 0 && now.Sub(c.modTime) > policy.Retention
		if expired {
			if removeRunLogDir(c.path, c.runID) {
				removed++
			}
			continue
		}
		survivors = append(survivors, c)
	}

	if policy.MaxRuns > 0 && len(survivors) > policy.MaxRuns {
		for _, c := range survivors[:len(survivors)-policy.MaxRuns] {
			if removeRunLogDir(c.path, c.runID) {
				removed++
			}
		}
	}

	if removed > 0 {
		slog.Info("reaped run logs", "root", root, "removed", removed)
	}
}

func removeRunLogDir(path, runID string) bool {
	if err := os.RemoveAll(path); err != nil {
		slog.Warn("failed to remove run log directory", "path", path, "run_id", runID, "error", err)
		return false
	}
	return true
}

func newestFileModTime(dir string) (time.Time, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return time.Time{}, false
	}
	var newest time.Time
	found := false
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if !found || info.ModTime().After(newest) {
			newest = info.ModTime()
			found = true
		}
	}
	return newest, found
}
