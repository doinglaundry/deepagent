// Package network: small networking helpers shared by sandbox / gateway.
// Currently a single function — GetFreePort — to avoid pulling a heavy
// dep when the only need is "pick a TCP port that won't collide".
package network

import (
	"fmt"
	"net"
)

// GetFreePort tries ports starting at base, then base+1, base+2, ... up to
// 50 attempts. Returns the first successful bind. If base is 0, asks the
// kernel for any free port via a 0-listen.
//
// Rationale: AIO sandbox containers want a stable base (e.g. 8081 for the
// first sandbox, 8082 for the next) so users can hit them from `curl` in
// dev. Falls back to kernel-assigned port if the range is exhausted (CI
// where 8081–8131 might all be busy).
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
