//go:build windows

package agent

import (
	"errors"
	"syscall"
)

func isTransientPIDOpenError(err error) bool {
	if err == nil {
		return false
	}
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	switch errno {
	case syscall.Errno(32):
		return true
	}
	return false
}

func isTransientPIDReplaceError(err error) bool {
	if err == nil {
		return false
	}
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	switch errno {
	case syscall.Errno(5), syscall.Errno(32):
		return true
	}
	return false
}
