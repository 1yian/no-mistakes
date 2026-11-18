//go:build unix

package signalguard

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/procreap"
)

type signalName struct {
	name string
	sig  syscall.Signal
}

var signalTable = []signalName{
	{"HUP", syscall.SIGHUP}, {"INT", syscall.SIGINT}, {"QUIT", syscall.SIGQUIT},
	{"ILL", syscall.SIGILL}, {"TRAP", syscall.SIGTRAP}, {"ABRT", syscall.SIGABRT},
	{"BUS", syscall.SIGBUS}, {"FPE", syscall.SIGFPE}, {"KILL", syscall.SIGKILL},
	{"USR1", syscall.SIGUSR1}, {"SEGV", syscall.SIGSEGV}, {"USR2", syscall.SIGUSR2},
	{"PIPE", syscall.SIGPIPE}, {"ALRM", syscall.SIGALRM}, {"TERM", syscall.SIGTERM},
	{"CHLD", syscall.SIGCHLD}, {"CONT", syscall.SIGCONT}, {"STOP", syscall.SIGSTOP},
	{"TSTP", syscall.SIGTSTP}, {"TTIN", syscall.SIGTTIN}, {"TTOU", syscall.SIGTTOU},
	{"URG", syscall.SIGURG}, {"XCPU", syscall.SIGXCPU}, {"XFSZ", syscall.SIGXFSZ},
	{"VTALRM", syscall.SIGVTALRM}, {"PROF", syscall.SIGPROF}, {"WINCH", syscall.SIGWINCH},
	{"IO", syscall.SIGIO}, {"SYS", syscall.SIGSYS},
}

const usage = "usage: kill [-s sigspec | -n signum | -sigspec] pid ... or kill -l [sigspec]"

type killRequest struct {
	signal  syscall.Signal
	targets []string
	list    bool
}

func RunHelper(args []string) (bool, int) {
	if len(args) < 3 || args[0] != helperArg {
		return false, 0
	}
	dir, tool, rest := args[1], args[2], args[3:]
	if tool == "kill" {
		return true, runKill(dir, rest, os.Stdout, os.Stderr)
	}
	message := fmt.Sprintf("%s: refused by no-mistakes: %s signals every matching process of this account on the host, including other runs' test suites, builds, and jobs. Signal only a process this run started: kill <pid> for a PID you launched yourself, such as $! of a background job.", tool, tool)
	fmt.Fprintln(os.Stderr, message)
	recordRefusal(dir, tool, rest, "name-wide signal")
	return true, 1
}

func recordRefusal(dir, tool string, args []string, reason string) {
	file, err := os.OpenFile(filepath.Join(dir, "refusals.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer file.Close()
	command := strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(tool + " " + strings.Join(args, " "))
	fmt.Fprintf(file, "%s\tpid=%d\tworktree=%s\t%s\t%s\n", time.Now().UTC().Format(time.RFC3339), os.Getppid(), os.Getenv(WorktreeEnv), command, reason)
}

func parseSignal(spec string) (syscall.Signal, error) {
	if n, err := strconv.Atoi(spec); err == nil {
		if n < 0 || n > 64 {
			return 0, fmt.Errorf("%s: invalid signal specification", spec)
		}
		return syscall.Signal(n), nil
	}
	name := strings.TrimPrefix(strings.ToUpper(spec), "SIG")
	for _, entry := range signalTable {
		if entry.name == name {
			return entry.sig, nil
		}
	}
	return 0, fmt.Errorf("%s: invalid signal specification", spec)
}

func parseKill(args []string) (killRequest, error) {
	req := killRequest{signal: syscall.SIGTERM}
	i := 0
	if i < len(args) {
		switch arg := args[i]; {
		case arg == "-l" || arg == "-L" || arg == "--list" || arg == "--table":
			req.list = true
			req.targets = args[i+1:]
			return req, nil
		case arg == "-s" || arg == "-n" || arg == "--signal":
			if i+1 >= len(args) {
				return req, fmt.Errorf("%s: option requires an argument", arg)
			}
			sig, err := parseSignal(args[i+1])
			if err != nil {
				return req, err
			}
			req.signal = sig
			i += 2
		case len(arg) > 1 && arg[0] == '-' && arg != "--":
			sig, err := parseSignal(arg[1:])
			if err != nil {
				return req, err
			}
			req.signal = sig
			i++
		}
	}
	if i < len(args) && args[i] == "--" {
		i++
	}
	req.targets = args[i:]
	if len(req.targets) == 0 {
		return req, errors.New(usage)
	}
	return req, nil
}

func runKill(dir string, args []string, stdout, stderr io.Writer) int {
	req, err := parseKill(args)
	if err != nil {
		fmt.Fprintf(stderr, "kill: %v\n", err)
		return 2
	}
	if req.list {
		return listSignals(req.targets, stdout, stderr)
	}
	var owned *procreap.Ownership
	status := 0
	for _, raw := range req.targets {
		pid, err := strconv.Atoi(raw)
		if err != nil {
			fmt.Fprintf(stderr, "kill: %s: arguments must be process or job IDs\n", raw)
			status = 1
			continue
		}
		if req.signal != 0 {
			if owned == nil {
				scope := procreap.Scope{Dir: os.Getenv(WorktreeEnv)}
				scope.Spawner, _ = strconv.Atoi(os.Getenv(SpawnerEnv))
				owned, err = procreap.ReadOwnership(scope, os.Getpid())
				if err != nil {
					fmt.Fprintf(stderr, "kill: refused by no-mistakes: cannot read the process table to check ownership: %v\n", err)
					return 1
				}
			}
			if reason := refusal(owned, pid); reason != "" {
				note := "A run may signal only its own agent tree and processes standing in its worktree; other runs' test suites and jobs share this host."
				if owned.Root() == 0 {
					note += " This shell is not inside a pipeline run's process tree."
				}
				fmt.Fprintf(stderr, "kill: refused by no-mistakes: %s %s\n", reason, note)
				recordRefusal(dir, "kill", args, reason)
				status = 1
				continue
			}
		}
		if err := syscall.Kill(pid, req.signal); err != nil {
			fmt.Fprintf(stderr, "kill: (%d) - %s\n", pid, errnoText(err))
			status = 1
		}
	}
	return status
}

func refusal(owned *procreap.Ownership, pid int) string {
	if pid == -1 {
		return "pid -1 would signal every process of this account."
	}
	var members []int
	group := 0
	switch {
	case pid > 0:
		if !owned.Exists(pid) {
			if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
				return ""
			}
			return fmt.Sprintf("pid %d is not a process of this run.", pid)
		}
		members = []int{pid}
	case pid == 0:
		group = syscall.Getpgrp()
		members = owned.Group(group)
	default:
		group = -pid
		members = owned.Group(group)
	}
	foreign := owned.Foreign(members)
	if len(foreign) == 0 {
		return ""
	}
	sort.Ints(foreign)
	if group == 0 {
		return fmt.Sprintf("pid %d is not a process of this run.", foreign[0])
	}
	return fmt.Sprintf("process group %d includes pid %d, which is not a process of this run.", group, foreign[0])
}

func errnoText(err error) string {
	switch {
	case errors.Is(err, syscall.ESRCH):
		return "No such process"
	case errors.Is(err, syscall.EPERM):
		return "Operation not permitted"
	}
	return err.Error()
}

func listSignals(specs []string, stdout, stderr io.Writer) int {
	if len(specs) == 0 {
		sorted := append([]signalName(nil), signalTable...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].sig < sorted[j].sig })
		names := make([]string, len(sorted))
		for i, entry := range sorted {
			names[i] = entry.name
		}
		fmt.Fprintln(stdout, strings.Join(names, " "))
		return 0
	}
	status := 0
	for _, spec := range specs {
		sig, err := parseSignal(spec)
		if err != nil {
			fmt.Fprintf(stderr, "kill: %v\n", err)
			status = 1
			continue
		}
		if _, err := strconv.Atoi(spec); err == nil {
			for _, entry := range signalTable {
				if entry.sig == sig {
					fmt.Fprintln(stdout, entry.name)
					break
				}
			}
			continue
		}
		fmt.Fprintln(stdout, int(sig))
	}
	return status
}
