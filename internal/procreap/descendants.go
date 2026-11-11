package procreap

import "strings"

type processState struct {
	PID  int
	PPID int
	PGID int
	Stat string
}

func LiveDescendants(pid int) (map[int]bool, error) {
	if pid <= 1 {
		return map[int]bool{}, nil
	}
	procs, err := listProcessStates()
	if err != nil {
		return nil, err
	}
	return liveDescendants(pid, procs), nil
}

func liveDescendants(root int, procs []processState) map[int]bool {
	byPID := make(map[int]processState, len(procs))
	for _, p := range procs {
		byPID[p.PID] = p
	}
	found := make(map[int]bool)
	for _, p := range procs {
		if p.PID == root || p.PID <= 1 || strings.HasPrefix(p.Stat, "Z") {
			continue
		}
		if p.PGID == root {
			found[p.PID] = true
			continue
		}
		seen := map[int]bool{p.PID: true}
		for parent := p.PPID; parent > 1 && !seen[parent]; {
			if parent == root {
				found[p.PID] = true
				break
			}
			seen[parent] = true
			next, ok := byPID[parent]
			if !ok {
				break
			}
			parent = next.PPID
		}
	}
	return found
}
