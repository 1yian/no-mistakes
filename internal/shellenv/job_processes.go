package shellenv

type jobProcessState int

const (
	jobProcessRunning jobProcessState = iota
	jobProcessExited
	jobProcessUnknown
)

type jobProcess struct {
	pid               uint32
	parentPID         uint32
	state             jobProcessState
	imagePath         string
	systemConsoleHost bool
}

func offendingJobProcesses(procs []jobProcess, leaderPID uint32) []jobProcess {
	var offenders []jobProcess
	for _, p := range procs {
		switch {
		case p.state == jobProcessExited:
		case p.state == jobProcessRunning && p.systemConsoleHost && p.parentPID == leaderPID:
		default:
			offenders = append(offenders, p)
		}
	}
	return offenders
}

func liveJobProcesses(procs []jobProcess, leaderPID uint32) int {
	return len(offendingJobProcesses(procs, leaderPID))
}

func jobHasLiveProcesses(totalBefore, totalAfter uint32, procs []jobProcess, leaderPID uint32) bool {
	return totalAfter != totalBefore || liveJobProcesses(procs, leaderPID) > 0
}
