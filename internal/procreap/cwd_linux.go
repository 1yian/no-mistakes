//go:build linux

package procreap

import (
	"os"
	"strconv"
)

func processCWDs(pids []int) map[int]string {
	cwds := make(map[int]string, len(pids))
	for _, pid := range pids {
		target, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/cwd")
		if err != nil {
			continue
		}
		cwds[pid] = trimDeletedSuffix(target)
	}
	return cwds
}
