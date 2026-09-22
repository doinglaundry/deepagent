// Package network has small networking helpers shared by sandbox.
package network

import (
	"fmt"
	"net"
)

// GetFreePort tries base..base+50, then falls back to a kernel-assigned port.
func GetFreePort(base int) (int, error) {
	if base <= 0 {
		return kernelAssignedPort()
	}
	for offset := range 50 {
		port := base + offset
		if isAvailable(port) {
			return port, nil
		}
	}
	return kernelAssignedPort()
}

// Listen on 0.0.0.0 (not 127.0.0.1) so colima/VM-published docker ports register as occupied.
func isAvailable(port int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

func kernelAssignedPort() (int, error) {
	l, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
