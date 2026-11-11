package daemon

import (
	"fmt"
	"os"
	"unsafe"

	"github.com/kunchenguid/no-mistakes/internal/paths"
	"golang.org/x/sys/windows"
)

var (
	modKernel32               = windows.NewLazySystemDLL("kernel32.dll")
	procGetConsoleProcessList = modKernel32.NewProc("GetConsoleProcessList")
	procFreeConsole           = modKernel32.NewProc("FreeConsole")
)

var consoleProcessCount = func() uint32 {
	var pids [2]uint32
	n, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	return uint32(n)
}

var freeConsole = func() error {
	if r, _, err := procFreeConsole.Call(); r == 0 {
		return err
	}
	return nil
}

func PrepareRunStdio() error {
	outputs := []struct {
		name      string
		id        uint32
		file      **os.File
		onConsole bool
	}{
		{name: "stdout", id: windows.STD_OUTPUT_HANDLE, file: &os.Stdout},
		{name: "stderr", id: windows.STD_ERROR_HANDLE, file: &os.Stderr},
	}
	// A released console's handles stay non-NULL in the standard slots, so
	// note which outputs are on the console before releasing it instead of
	// relying on every Windows build failing those handles afterwards.
	for i := range outputs {
		outputs[i].onConsole = isConsoleHandle(stdHandle(outputs[i].id))
	}
	released := releaseSoleConsole()

	var log *os.File
	for _, out := range outputs {
		if usableHandle(stdHandle(out.id)) && !(released && out.onConsole) {
			continue
		}
		if log == nil {
			f, err := openBootstrapLogForStdio()
			if err != nil {
				return err
			}
			// Held for the process lifetime, like any standard handle.
			log = f
		}
		if err := windows.SetStdHandle(out.id, windows.Handle(log.Fd())); err != nil {
			return fmt.Errorf("point %s at the daemon bootstrap log: %w", out.name, err)
		}
		*out.file = log
	}
	return nil
}

func releaseSoleConsole() bool {
	if consoleProcessCount() != 1 {
		return false
	}
	return freeConsole() == nil
}

func stdHandle(id uint32) windows.Handle {
	h, err := windows.GetStdHandle(id)
	if err != nil {
		return windows.InvalidHandle
	}
	return h
}

func isConsoleHandle(h windows.Handle) bool {
	var mode uint32
	return h != 0 && h != windows.InvalidHandle && windows.GetConsoleMode(h, &mode) == nil
}

func usableHandle(h windows.Handle) bool {
	if h == 0 || h == windows.InvalidHandle {
		return false
	}
	kind, err := windows.GetFileType(h)
	return err == nil && kind != windows.FILE_TYPE_UNKNOWN
}

func openBootstrapLogForStdio() (*os.File, error) {
	p, err := paths.New()
	if err != nil {
		return nil, fmt.Errorf("resolve paths: %w", err)
	}
	if err := p.EnsureDirs(); err != nil {
		return nil, fmt.Errorf("create directories: %w", err)
	}
	f, err := os.OpenFile(p.DaemonBootstrapLog(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open daemon bootstrap log: %w", err)
	}
	return f, nil
}
