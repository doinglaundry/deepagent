// Package network has small networking helpers shared by sandbox / gateway.
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

func isAvailable(port int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

func kernelAssignedPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
