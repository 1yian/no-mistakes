//go:build windows

package daemon

import (
	"os"

	"golang.org/x/sys/windows"
)

const lockByteOffset = 0xFFFFFFFF

func tryLockFile(f *os.File) error {
	ol := &windows.Overlapped{Offset: lockByteOffset}
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, ol,
	)
}

func unlockFile(f *os.File) error {
	ol := &windows.Overlapped{Offset: lockByteOffset}
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ol)
}
