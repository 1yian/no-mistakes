//go:build windows

package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

var (
	kernel32      = windows.NewLazySystemDLL("kernel32.dll")
	attachConsole = kernel32.NewProc("AttachConsole")
	freeConsole   = kernel32.NewProc("FreeConsole")
)

func main() {
	result, err := probe()
	if err != nil {
		fmt.Fprintf(os.Stdout, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stdout, result)
}

func probe() (string, error) {
	hide := flag.Bool("hide", false, "start a console window hidden")
	flag.Parse()
	if flag.NArg() == 0 {
		return "", errors.New("usage: consoleprobe [-hide] <exe> [args...]")
	}
	cmd := exec.Command(flag.Arg(0), flag.Args()[1:]...)
	if *hide {
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "ready\n" {
		return "", fmt.Errorf("child readiness: line %q, err %v", line, err)
	}

	// Windows allocates a console before the child's entry point runs, so
	// the answer is settled once the child reported ready.
	r, _, callErr := attachConsole.Call(uintptr(cmd.Process.Pid))
	if r != 0 {
		_, _, _ = freeConsole.Call()
		return "console", nil
	}
	if errors.Is(callErr, windows.ERROR_INVALID_HANDLE) {
		return "no-console", nil
	}
	return "", fmt.Errorf("AttachConsole(%d): %w", cmd.Process.Pid, callErr)
}
