//go:build !windows

package ipc

import (
	"fmt"
	"net"
	"os"
	"syscall"
	"time"
)

func listen(endpoint string) (net.Listener, error) {
	if err := CheckEndpointPath(endpoint); err != nil {
		return nil, err
	}
	if conn, err := net.DialTimeout("unix", endpoint, 200*time.Millisecond); err == nil {
		conn.Close()
		return nil, fmt.Errorf("ipc socket %s is already in use by a live listener", endpoint)
	}
	_ = os.Remove(endpoint)
	oldMask := syscall.Umask(0o077)
	ln, err := net.Listen("unix", endpoint)
	syscall.Umask(oldMask)
	return ln, err
}

func dial(endpoint string, timeout time.Duration) (net.Conn, error) {
	if err := CheckEndpointPath(endpoint); err != nil {
		return nil, err
	}
	return dialNetworkWithTimeout("unix", endpoint, timeout)
}

var maxEndpointPathLen = len(syscall.RawSockaddrUnix{}.Path) - 1

func CheckEndpointPath(endpoint string) error {
	if len(endpoint) > maxEndpointPathLen {
		return &SocketPathTooLongError{Path: endpoint, Limit: maxEndpointPathLen}
	}
	return nil
}
