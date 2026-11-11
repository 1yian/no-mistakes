//go:build unix && !linux

package procreap

func processCWDs(pids []int) map[int]string {
	if len(pids) == 0 {
		return nil
	}
	return lsofCWDs(pids)
}
