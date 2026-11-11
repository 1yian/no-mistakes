package procreap

import (
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/worktrees"
)

const (
	DefaultMinAge = 10 * time.Minute

	DefaultGrace = 3 * time.Second
)

type Process struct {
	PID     int
	PPID    int
	PGID    int
	Command string
	Elapsed time.Duration
}

type Victim struct {
	PID      int
	PGID     int
	Command  string
	Worktree string
	Killed   bool // true when SIGTERM was not enough and SIGKILL was needed
}

type Worktree struct {
	Dir    string
	RepoID string
	RunID  string
}

type Options struct {
	WorktreesRoot string

	Worktrees []Worktree

	Scopes []string

	MinAge time.Duration

	RunActive func(repoID, runID string) bool

	Grace time.Duration
}

var (
	listProcessesFunc = listProcesses
	processCWDsFunc   = processCWDs
	signalProcessFunc = signalProcess
	signalGroupFunc   = signalGroup
	processAliveFunc  = processAlive
)

func Sweep(opts Options) ([]Victim, error) {
	if strings.TrimSpace(opts.WorktreesRoot) == "" && len(opts.Worktrees) == 0 {
		return nil, nil
	}
	procs, err := listProcessesFunc()
	if err != nil {
		return nil, err
	}
	if len(procs) == 0 {
		return nil, nil
	}

	byPID := make(map[int]Process, len(procs))
	for _, p := range procs {
		byPID[p.PID] = p
	}
	protected := protectedPIDs(byPID)

	candidates := make([]int, 0, len(procs))
	for _, p := range procs {
		if p.PID <= 1 || protected[p.PID] {
			continue
		}
		if len(opts.Scopes) == 0 && opts.MinAge > 0 && p.Elapsed < opts.MinAge {
			continue
		}
		candidates = append(candidates, p.PID)
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	cwds := processCWDsFunc(candidates)
	matchers := worktreeMatchers(opts)
	var scopes []string
	for _, scope := range opts.Scopes {
		scopes = append(scopes, pathPrefixes(scope)...)
	}

	matched := make(map[int]string)
	for pid, cwd := range cwds {
		dir, repoID, runID, ok := worktreeRef(matchers, cwd)
		if !ok {
			continue
		}
		if len(scopes) > 0 {
			if !underAny(scopes, cwd) {
				continue
			}
		} else if opts.RunActive != nil && opts.RunActive(repoID, runID) {
			continue
		}
		matched[pid] = dir
	}
	if len(matched) == 0 {
		return nil, nil
	}

	victims := expandVictims(matched, procs, protected)
	terminate(victims, opts.Grace)
	return victims, nil
}

func SweepAndLog(opts Options, reason string) {
	victims, err := Sweep(opts)
	if err != nil {
		slog.Warn("orphan process sweep failed", "reason", reason, "error", err)
		return
	}
	for _, v := range victims {
		slog.Info("reaped orphaned run process",
			"reason", reason,
			"pid", v.PID,
			"worktree", v.Worktree,
			"command", v.Command,
			"sigkill", v.Killed)
	}
}

func SweepRunWorktree(worktreesRoot, repoID, runID, dir string, reason string) {
	SweepRunWorktrees(worktreesRoot, []Worktree{{Dir: dir, RepoID: repoID, RunID: runID}}, reason)
}

func SweepRunWorktrees(worktreesRoot string, wts []Worktree, reason string) {
	scopes := make([]string, 0, len(wts))
	for _, wt := range wts {
		if strings.TrimSpace(wt.Dir) == "" {
			continue
		}
		scopes = append(scopes, wt.Dir)
	}
	if len(scopes) == 0 {
		return
	}
	SweepAndLog(Options{
		WorktreesRoot: worktreesRoot,
		Worktrees:     wts,
		Scopes:        scopes,
	}, reason)
}

func protectedPIDs(byPID map[int]Process) map[int]bool {
	protected := map[int]bool{os.Getpid(): true}
	pid := os.Getpid()
	for range len(byPID) + 1 {
		p, ok := byPID[pid]
		if !ok || p.PPID <= 1 || protected[p.PPID] {
			break
		}
		protected[p.PPID] = true
		pid = p.PPID
	}
	return protected
}

func pathPrefixes(dir string) []string {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	cleaned := filepath.Clean(dir)
	out := []string{cleaned}
	if canonical := worktrees.Canonical(cleaned); canonical != cleaned {
		out = append(out, canonical)
	}
	return out
}

type worktreeMatcher struct {
	prefix string
	named  bool
	repoID string
	runID  string
}

func worktreeMatchers(opts Options) []worktreeMatcher {
	var out []worktreeMatcher
	for _, prefix := range pathPrefixes(opts.WorktreesRoot) {
		out = append(out, worktreeMatcher{prefix: prefix})
	}
	for _, wt := range opts.Worktrees {
		if strings.TrimSpace(wt.Dir) == "" || strings.TrimSpace(wt.RepoID) == "" || strings.TrimSpace(wt.RunID) == "" {
			continue
		}
		for _, prefix := range pathPrefixes(wt.Dir) {
			out = append(out, worktreeMatcher{prefix: prefix, named: true, repoID: wt.RepoID, runID: wt.RunID})
		}
	}
	return out
}

func worktreeRef(matchers []worktreeMatcher, path string) (dir, repoID, runID string, ok bool) {
	if path == "" {
		return "", "", "", false
	}
	cleaned := filepath.Clean(path)
	for _, matcher := range matchers {
		rel, err := filepath.Rel(matcher.prefix, cleaned)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		if matcher.named {
			return matcher.prefix, matcher.repoID, matcher.runID, true
		}
		if rel == "." {
			continue
		}
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
			continue
		}
		return filepath.Join(matcher.prefix, parts[0], parts[1]), parts[0], parts[1], true
	}
	return "", "", "", false
}

func underAny(prefixes []string, path string) bool {
	cleaned := filepath.Clean(path)
	for _, prefix := range prefixes {
		rel, err := filepath.Rel(prefix, cleaned)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		return true
	}
	return false
}

func expandVictims(matched map[int]string, procs []Process, protected map[int]bool) []Victim {
	selected := make(map[int]string, len(matched))
	for pid, dir := range matched {
		selected[pid] = dir
	}

	children := make(map[int][]Process, len(procs))
	byGroup := make(map[int][]Process, len(procs))
	for _, p := range procs {
		children[p.PPID] = append(children[p.PPID], p)
		byGroup[p.PGID] = append(byGroup[p.PGID], p)
	}

	queue := make([]int, 0, len(matched))
	for pid := range matched {
		queue = append(queue, pid)
	}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		dir := selected[pid]
		related := append(append([]Process(nil), children[pid]...), byGroup[pid]...)
		for _, p := range related {
			if p.PID <= 1 || protected[p.PID] || p.PID == pid {
				continue
			}
			if _, seen := selected[p.PID]; seen {
				continue
			}
			selected[p.PID] = dir
			queue = append(queue, p.PID)
		}
	}

	info := make(map[int]Process, len(procs))
	for _, p := range procs {
		info[p.PID] = p
	}
	victims := make([]Victim, 0, len(selected))
	for pid, dir := range selected {
		p := info[pid]
		victims = append(victims, Victim{PID: pid, PGID: p.PGID, Command: p.Command, Worktree: dir})
	}
	sort.Slice(victims, func(i, j int) bool { return victims[i].PID < victims[j].PID })
	return victims
}

func terminate(victims []Victim, grace time.Duration) {
	if grace <= 0 {
		grace = DefaultGrace
	}
	for _, v := range victims {
		signalVictim(v, sigTerm)
	}
	deadline := time.Now().Add(grace)
	for {
		remaining := false
		for _, v := range victims {
			if processAliveFunc(v.PID) {
				remaining = true
				break
			}
		}
		if !remaining || !time.Now().Before(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	for i := range victims {
		if !processAliveFunc(victims[i].PID) {
			continue
		}
		victims[i].Killed = true
		signalVictim(victims[i], sigKill)
	}
}

func signalVictim(v Victim, sig procSignal) {
	_ = signalProcessFunc(v.PID, sig)
	if v.PGID == v.PID && v.PGID > 1 {
		_ = signalGroupFunc(v.PGID, sig)
	}
}
