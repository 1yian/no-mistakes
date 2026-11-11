//go:build windows

package update

import (
	"syscall"

	"golang.org/x/sys/windows"
)

func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
	}
}
