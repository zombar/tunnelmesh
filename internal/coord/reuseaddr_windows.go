//go:build windows

package coord

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// setReuseAddr sets SO_REUSEADDR on the socket to allow binding to a specific IP
// even when another socket is bound to 0.0.0.0 on the same port.
func setReuseAddr(network, address string, c syscall.RawConn) error {
	var setSockOptErr error
	err := c.Control(func(fd uintptr) {
		setSockOptErr = windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, windows.SO_REUSEADDR, 1)
	})
	if err != nil {
		return err
	}
	return setSockOptErr
}
