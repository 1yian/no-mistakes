package daemon

import (
	"context"
	"log/slog"
	"os"
	"sort"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/procreap"
)

type worktreeReapPolicy struct {
	Retention time.Duration
	MaxRuns   int
}

func worktreeReapPolicyFor(global *config.GlobalConfig) worktreeReapPolicy {
	policy := worktreeReapPolicy{
		Retention: config.DefaultWorktreeRetention,
		MaxRuns:   config.DefaultWorktreeMaxRuns,
	}
	if global == nil {
		return policy
	}
	resolved := config.Merge(global, &config.RepoConfig{})
	policy.Retention = resolved.Worktree.Retention
	policy.MaxRuns = resolved.Worktree.MaxRuns
	return policy
}

func reapWorktrees(d *db.DB, p *paths.Paths, policy worktreeReapPolicy, now time.Time) {
	removable, _ := defaultTreeOrphanWorktrees(d, p)
	if len(removable) == 0 {
		return
	}
	toRemove := worktreeReapCandidates(removable, policy, now)
	if len(toRemove) == 0 {
		return
	}

	// Only the directories actually selected for removal are swept: sweeping
	// a retained worktree would kill processes still using a checkout the
	// policy was meant to keep.
	sweepable := make([]procreap.Worktree, 0, len(toRemove))
	for _, wt := range toRemove {
		sweepable = append(sweepable, procreap.Worktree{Dir: wt.dir, RepoID: wt.repoID, RunID: wt.runID})
	}
	sweepRunWorktrees(p.WorktreesDir(), sweepable, "worktree_reap")

	removed := 0
	for _, wt := range toRemove {
		if removeOrphanWorktree(context.Background(), wt) {
			removed++
		}
	}

	if removed > 0 {
		slog.Info("reaped leftover run worktrees", "removed", removed)
	}
}

func worktreeReapCandidates(removable []orphanWorktree, policy worktreeReapPolicy, now time.Time) []orphanWorktree {
	type candidate struct {
		wt      orphanWorktree
		modTime time.Time
	}
	candidates := make([]candidate, 0, len(removable))
	for _, wt := range removable {
		info, err := os.Stat(wt.dir)
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate{wt: wt, modTime: info.ModTime()})
	}
	if len(candidates) == 0 {
		return nil
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].modTime.Before(candidates[j].modTime)
	})

	var toRemove []candidate
	survivors := make([]candidate, 0, len(candidates))
	for _, c := range candidates {
		expired := policy.Retention > 0 && now.Sub(c.modTime) > policy.Retention
		if expired {
			toRemove = append(toRemove, c)
			continue
		}
		survivors = append(survivors, c)
	}

	if policy.MaxRuns > 0 && len(survivors) > policy.MaxRuns {
		toRemove = append(toRemove, survivors[:len(survivors)-policy.MaxRuns]...)
	}

	out := make([]orphanWorktree, 0, len(toRemove))
	for _, c := range toRemove {
		out = append(out, c.wt)
	}
	return out
}
