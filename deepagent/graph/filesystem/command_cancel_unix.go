//go:build !windows

package filesystem

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func configureShellCommandCancel(shellCommand *exec.Cmd) {
	shellCommand.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	shellCommand.WaitDelay = 500 * time.Millisecond
	shellCommand.Cancel = func() error {
		if shellCommand.Process == nil {
			return nil
		}
		err := syscall.Kill(-shellCommand.Process.Pid, syscall.SIGKILL)
		if err == nil || errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return shellCommand.Process.Kill()
	}
}
