//go:build unix

package procreap

import (
	"path/filepath"
	"strings"
)

type Scope struct {
	Spawner int
	Dir     string
}

type Ownership struct {
	scope   Scope
	procs   map[int]processState
	lineage map[int]bool
	root    int
}

func ReadOwnership(scope Scope, from int) (*Ownership, error) {
	table, err := listProcessStates()
	if err != nil {
		return nil, err
	}
	o := &Ownership{scope: scope, procs: make(map[int]processState, len(table))}
	for _, p := range table {
		o.procs[p.PID] = p
	}
	o.root = o.rootAbove(from)
	if o.root > 1 {
		o.lineage = descendantsOf(o.root, table, true)
		o.lineage[o.root] = true
	}
	return o, nil
}

func (o *Ownership) rootAbove(from int) int {
	if o.scope.Spawner <= 1 {
		return 0
	}
	seen := map[int]bool{}
	for pid := from; pid > 1 && !seen[pid]; {
		seen[pid] = true
		p, ok := o.procs[pid]
		if !ok {
			return 0
		}
		if p.PPID == o.scope.Spawner {
			return pid
		}
		pid = p.PPID
	}
	return 0
}

func (o *Ownership) Root() int { return o.root }

func (o *Ownership) Exists(pid int) bool {
	_, ok := o.procs[pid]
	return ok
}

func (o *Ownership) Group(pgid int) []int {
	var members []int
	for pid, p := range o.procs {
		if p.PGID == pgid {
			members = append(members, pid)
		}
	}
	return members
}

func (o *Ownership) Foreign(pids []int) []int {
	standing := o.standingInWorktree(pids)
	var foreign []int
	for _, pid := range pids {
		if pid > 1 && (o.lineage[pid] || standing[pid]) {
			continue
		}
		foreign = append(foreign, pid)
	}
	return foreign
}

func (o *Ownership) standingInWorktree(pids []int) map[int]bool {
	dir := strings.TrimSpace(o.scope.Dir)
	if dir == "" || filepath.Clean(dir) == string(filepath.Separator) {
		return nil
	}
	var candidates []int
	for _, pid := range pids {
		if pid > 1 && !o.lineage[pid] {
			candidates = append(candidates, pid)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	prefixes := pathPrefixes(dir)
	standing := make(map[int]bool)
	for pid, cwd := range processCWDsFunc(candidates) {
		if cwd != "" && underAny(prefixes, cwd) {
			standing[pid] = true
		}
	}
	return standing
}
