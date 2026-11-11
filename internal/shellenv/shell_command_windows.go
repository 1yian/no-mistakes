//go:build windows

package shellenv

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/kunchenguid/no-mistakes/internal/winproc"
	"golang.org/x/sys/windows"
)

const windowsTerminateGrace = 3 * time.Second

const taskkillExitNoSuchProcess = 128

const defaultWaitDelay = 5 * time.Second

type shellCommandJobState struct {
	handle              windows.Handle
	cancelEvent         windows.Handle
	cancelEventName     string
	cooperative         bool
	assigned            atomic.Bool
	cooperativeDeadline atomic.Pointer[time.Time]
}

type jobObjectBasicAccountingInformation struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

var shellCommandJobs sync.Map
var shellCommandJobSetupErrors sync.Map

var newShellCommandJobFunc = newShellCommandJob
var assignShellCommandJobFunc = assignShellCommandJob
var resumeProcessThreadsFunc = resumeProcessThreads
var windowsCooperativeHelperCommandFunc = windowsCooperativeHelperCommand

func ConfigureShellCommand(cmd *exec.Cmd) {
	configureWindowsShellCommand(cmd, false)
}

func ConfigureCooperativeShellCommand(cmd *exec.Cmd) {
	configureWindowsShellCommand(cmd, true)
}

func configureWindowsShellCommand(cmd *exec.Cmd, cooperative bool) {
	// Suppress the visible console window Windows would otherwise allocate for
	// this console child (agents, cmd.exe shell steps) when spawned from the
	// console-less daemon. See issue #287. Harden allocates SysProcAttr if
	// needed; the creation flags are then adjusted for the requested topology.
	winproc.Harden(cmd)
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	if cooperative {
		cmd.SysProcAttr.CreationFlags &^= windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS
		cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_CONSOLE
	} else {
		cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP
	}
	if job, err := newShellCommandJobFunc(); err == nil {
		state := &shellCommandJobState{handle: job, cooperative: cooperative}
		if cooperative {
			cancelEvent, cancelEventName, eventErr := newWindowsCooperativeCancelEvent()
			if eventErr != nil {
				_ = windows.CloseHandle(job)
				shellCommandJobSetupErrors.Store(cmd, eventErr)
			} else {
				state.cancelEvent = cancelEvent
				state.cancelEventName = cancelEventName
				shellCommandJobs.Store(cmd, state)
				cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
			}
		} else {
			shellCommandJobs.Store(cmd, state)
			cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
		}
	} else {
		shellCommandJobSetupErrors.Store(cmd, err)
	}

	// Install a WaitDelay backstop unless the caller has chosen one
	// explicitly (the short login-shell probe, for example, uses a tighter
	// bound of its own).
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = defaultWaitDelay
		if cooperative {
			cmd.WaitDelay = windowsTerminateGrace
		}
	}

	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		if job, ok := shellCommandJob(cmd); ok && job.assigned.Load() {
			if job.cooperative && job.cancelEvent != 0 {
				if err := windows.SetEvent(job.cancelEvent); err == nil {
					deadline := time.Now().Add(windowsTerminateGrace)
					job.cooperativeDeadline.Store(&deadline)
					return nil
				}
			}
			terminateShellCommandJob(cmd, false)
			return nil
		}
		return forceKillShellCommand(cmd)
	}
}

func StartShellCommand(cmd *exec.Cmd) error {
	if err, ok := takeShellCommandJobSetupError(cmd); ok {
		return fmt.Errorf("windows job object setup: %w", err)
	}
	if job, ok := shellCommandJob(cmd); ok && job.cooperative {
		if err := wrapWindowsCooperativeCommand(cmd, job.cancelEventName); err != nil {
			closeShellCommandJob(cmd)
			return err
		}
	}
	if err := cmd.Start(); err != nil {
		closeShellCommandJob(cmd)
		return err
	}
	job, ok := shellCommandJob(cmd)
	if !ok {
		return nil
	}
	if err := assignShellCommandJobFunc(job.handle, uint32(cmd.Process.Pid)); err != nil {
		return failStartedShellCommand(cmd, fmt.Errorf("assign process to job object: %w", err))
	}
	job.assigned.Store(true)
	if err := resumeProcessThreadsFunc(uint32(cmd.Process.Pid)); err != nil {
		return failStartedShellCommand(cmd, err)
	}
	return nil
}

func TerminateShellCommandGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if job, ok := shellCommandJob(cmd); ok && job.assigned.Load() {
		if deadline := job.cooperativeDeadline.Load(); deadline != nil {
			waitForWindowsJobExit(job.handle, time.Until(*deadline))
		}
		terminateShellCommandJob(cmd, true)
		return
	}
	_ = forceKillShellCommand(cmd)
}

func forceKillShellCommand(cmd *exec.Cmd) error {
	pid := strconv.Itoa(cmd.Process.Pid)
	kill := exec.Command("taskkill", "/T", "/F", "/PID", pid)
	winproc.Harden(kill)
	err := kill.Run()
	switch {
	case err == nil:
		return nil
	case errors.Is(err, exec.ErrNotFound):
	case isTaskkillAlreadyGone(err):
		return os.ErrProcessDone
	default:
	}
	if killErr := cmd.Process.Kill(); killErr != nil {
		if errors.Is(killErr, os.ErrProcessDone) {
			return os.ErrProcessDone
		}
		return fmt.Errorf("taskkill /PID %s: %w; process kill: %v", pid, err, killErr)
	}
	if err != nil {
		return fmt.Errorf("taskkill /PID %s: %w", pid, err)
	}
	return nil
}

func ShellCommandGroupActive(cmd *exec.Cmd, window time.Duration) (active bool, detail string, err error) {
	job, ok := shellCommandJob(cmd)
	if !ok || !job.assigned.Load() || cmd.Process == nil {
		return false, "", errors.New("command has no assigned job")
	}
	leaderPID := uint32(cmd.Process.Pid)
	deadline := time.Now().Add(window)
	var lastMembers []groupMember
	for {
		live, members, err := probeShellCommandJob(job.handle, leaderPID)
		if len(members) > 0 {
			lastMembers = members
		}
		if err != nil {
			return live, formatGroupMembers(lastMembers), err
		}
		if !live || !time.Now().Before(deadline) {
			return live, formatGroupMembers(members), nil
		}
		time.Sleep(groupProbeInterval)
	}
}

func probeShellCommandJob(job windows.Handle, leaderPID uint32) (bool, []groupMember, error) {
	totalBefore, err := jobTotalProcesses(job)
	if err != nil {
		return false, nil, err
	}
	procs, err := inspectJobProcesses(job)
	if err != nil {
		return false, nil, err
	}
	totalAfter, err := jobTotalProcesses(job)
	if err != nil {
		return false, nil, err
	}
	offenders := offendingJobProcesses(procs, leaderPID)
	members := make([]groupMember, 0, len(offenders))
	for _, p := range offenders {
		members = append(members, groupMember{pid: int(p.pid), command: p.imagePath})
	}
	return jobHasLiveProcesses(totalBefore, totalAfter, procs, leaderPID), members, nil
}

func jobTotalProcesses(job windows.Handle) (uint32, error) {
	var info jobObjectBasicAccountingInformation
	err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil)
	if err != nil {
		return 0, fmt.Errorf("read job accounting: %w", err)
	}
	return info.TotalProcesses, nil
}

const stillActiveExitCode = 259

const maxJobProcessList = 1 << 16

var procIsProcessInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

type jobObjectBasicProcessIDList struct {
	NumberOfAssignedProcesses uint32
	NumberOfProcessIdsInList  uint32
	ProcessIdList             [1]uintptr
}

func jobProcessIDs(job windows.Handle) ([]uint32, error) {
	for capacity := 16; capacity <= maxJobProcessList; capacity *= 4 {
		size := int(unsafe.Offsetof(jobObjectBasicProcessIDList{}.ProcessIdList)) + capacity*int(unsafe.Sizeof(uintptr(0)))
		// uint64 elements keep the buffer aligned for the struct header.
		buf := make([]uint64, (size+7)/8)
		list := (*jobObjectBasicProcessIDList)(unsafe.Pointer(&buf[0]))
		err := windows.QueryInformationJobObject(job, windows.JobObjectBasicProcessIdList,
			uintptr(unsafe.Pointer(list)), uint32(len(buf)*8), nil)
		if errors.Is(err, windows.ERROR_MORE_DATA) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("list job processes: %w", err)
		}
		first := unsafe.Add(unsafe.Pointer(list), unsafe.Offsetof(list.ProcessIdList))
		ids := make([]uint32, 0, list.NumberOfProcessIdsInList)
		for i := uintptr(0); i < uintptr(list.NumberOfProcessIdsInList); i++ {
			ids = append(ids, uint32(*(*uintptr)(unsafe.Add(first, i*unsafe.Sizeof(uintptr(0))))))
		}
		return ids, nil
	}
	return nil, errors.New("list job processes: the job holds too many processes to inspect")
}

func inspectJobProcesses(job windows.Handle) ([]jobProcess, error) {
	ids, err := jobProcessIDs(job)
	if err != nil {
		return nil, err
	}
	systemDir, err := windows.GetSystemDirectory()
	if err != nil {
		return nil, fmt.Errorf("resolve system directory: %w", err)
	}
	consoleHost := filepath.Join(systemDir, "conhost.exe")
	procs := make([]jobProcess, 0, len(ids))
	for _, pid := range ids {
		procs = append(procs, inspectJobProcess(job, pid, consoleHost))
	}
	return procs, nil
}

func inspectJobProcess(job windows.Handle, pid uint32, consoleHost string) jobProcess {
	proc := jobProcess{pid: pid, state: jobProcessUnknown}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			// No process has this id any more.
			proc.state = jobProcessExited
		}
		return proc
	}
	defer windows.CloseHandle(handle)

	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return proc
	}
	if exitCode != stillActiveExitCode {
		proc.state = jobProcessExited
		return proc
	}
	var inJob int32
	if ret, _, _ := procIsProcessInJob.Call(uintptr(handle), uintptr(job), uintptr(unsafe.Pointer(&inJob))); ret == 0 {
		return proc
	}
	if inJob == 0 {
		proc.state = jobProcessExited
		return proc
	}
	proc.state = jobProcessRunning

	var basic windows.PROCESS_BASIC_INFORMATION
	if err := windows.NtQueryInformationProcess(handle, windows.ProcessBasicInformation,
		unsafe.Pointer(&basic), uint32(unsafe.Sizeof(basic)), nil); err == nil {
		proc.parentPID = uint32(basic.InheritedFromUniqueProcessId)
	}
	var path [windows.MAX_LONG_PATH]uint16
	size := uint32(len(path))
	if err := windows.QueryFullProcessImageName(handle, 0, &path[0], &size); err == nil {
		proc.imagePath = windows.UTF16ToString(path[:size])
		proc.systemConsoleHost = strings.EqualFold(proc.imagePath, consoleHost)
	}
	return proc
}

func waitForWindowsJobExit(job windows.Handle, window time.Duration) {
	if window <= 0 {
		return
	}
	deadline := time.Now().Add(window)
	for {
		var info jobObjectBasicAccountingInformation
		err := windows.QueryInformationJobObject(
			job,
			windows.JobObjectBasicAccountingInformation,
			uintptr(unsafe.Pointer(&info)),
			uint32(unsafe.Sizeof(info)),
			nil,
		)
		if err != nil || info.ActiveProcesses == 0 || !time.Now().Before(deadline) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func newShellCommandJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	ret, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	if ret == 0 {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

func newWindowsCooperativeCancelEvent() (windows.Handle, string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return 0, "", err
	}
	name := `Local\no-mistakes-cooperative-` + hex.EncodeToString(id[:])
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, "", err
	}
	event, err := windows.CreateEvent(nil, 1, 0, namePtr)
	if err != nil {
		return 0, "", err
	}
	return event, name, nil
}

func wrapWindowsCooperativeCommand(cmd *exec.Cmd, eventName string) error {
	if cmd == nil || cmd.Path == "" || len(cmd.Args) == 0 {
		return errors.New("invalid Windows cooperative command")
	}
	if cmd.SysProcAttr != nil && cmd.SysProcAttr.CmdLine != "" {
		return errors.New("Windows cooperative commands do not support SysProcAttr.CmdLine")
	}
	path, args, err := windowsCooperativeHelperCommandFunc(eventName, cmd.Path, cmd.Args)
	if err != nil {
		return fmt.Errorf("prepare Windows cooperative command helper: %w", err)
	}
	cmd.Path = path
	cmd.Args = args
	return nil
}

func windowsCooperativeHelperCommand(eventName, targetPath string, targetArgs []string) (string, []string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", nil, err
	}
	args := []string{exe, windowsCooperativeCommandArg + eventName, targetPath}
	args = append(args, targetArgs...)
	return exe, args, nil
}

func shellCommandJob(cmd *exec.Cmd) (*shellCommandJobState, bool) {
	if cmd == nil {
		return nil, false
	}
	value, ok := shellCommandJobs.Load(cmd)
	if !ok {
		return nil, false
	}
	job, ok := value.(*shellCommandJobState)
	return job, ok
}

func takeShellCommandJobSetupError(cmd *exec.Cmd) (error, bool) {
	if cmd == nil {
		return nil, false
	}
	value, ok := shellCommandJobSetupErrors.LoadAndDelete(cmd)
	if !ok {
		return nil, false
	}
	err, ok := value.(error)
	return err, ok
}

func closeShellCommandJob(cmd *exec.Cmd) bool {
	if cmd == nil {
		return false
	}
	value, ok := shellCommandJobs.LoadAndDelete(cmd)
	if !ok {
		return false
	}
	job, ok := value.(*shellCommandJobState)
	if !ok {
		return false
	}
	if job.cancelEvent != 0 {
		_ = windows.CloseHandle(job.cancelEvent)
	}
	_ = windows.CloseHandle(job.handle)
	return true
}

func terminateShellCommandJob(cmd *exec.Cmd, closeJob bool) bool {
	job, ok := shellCommandJob(cmd)
	if !ok {
		return false
	}
	assigned := job.assigned.Load()
	if assigned {
		_ = windows.TerminateJobObject(job.handle, 1)
	}
	if closeJob {
		closeShellCommandJob(cmd)
	}
	return assigned
}

func assignShellCommandJob(job windows.Handle, pid uint32) error {
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process)
	return windows.AssignProcessToJobObject(job, process)
}

func resumeProcessThreads(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)

	var entry windows.ThreadEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Thread32First(snapshot, &entry); err != nil {
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			return nil
		}
		return err
	}

	resumed := false
	for {
		if entry.OwnerProcessID == pid {
			thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if err == nil {
				_, err = windows.ResumeThread(thread)
				_ = windows.CloseHandle(thread)
				if err != nil {
					return err
				}
				resumed = true
			} else if !errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
				return err
			}
		}
		if err := windows.Thread32Next(snapshot, &entry); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				break
			}
			return err
		}
	}
	if !resumed {
		return fmt.Errorf("no suspended threads found for pid %d", pid)
	}
	return nil
}

func failStartedShellCommand(cmd *exec.Cmd, cause error) error {
	terminateShellCommandJob(cmd, true)
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	return cause
}

func isTaskkillAlreadyGone(err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	return exitErr.ExitCode() == taskkillExitNoSuchProcess
}

func detachFromTerminal(cmd *exec.Cmd) {}
