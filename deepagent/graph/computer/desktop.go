//go:build !windows

package computer

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// Desktop owns one native helper. The helper holds the machine-wide Run lock.
type Desktop struct {
	mu         sync.Mutex
	helperPath string
	command    *exec.Cmd
	input      io.WriteCloser
	output     *bufio.Scanner
	closed     bool
}

func NewDesktop(ctx context.Context) (*Desktop, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	desktop := &Desktop{helperPath: filepath.Join(filepath.Dir(executable), "deepagent-computer")}
	_, err = desktop.PerformAction(ctx, "", "ping", Action{})
	if err != nil {
		desktop.Close(context.Background())
		return nil, err
	}
	return desktop, nil
}

func (desktop *Desktop) PerformAction(ctx context.Context, ownerRunID, operation string, action Action) (*Observation, error) {
	desktop.mu.Lock()
	defer desktop.mu.Unlock()
	if desktop.closed {
		return nil, errors.New("desktop is closed")
	}
	err := ctx.Err()
	if err != nil {
		return nil, err
	}
	if desktop.command == nil {
		command := exec.Command(desktop.helperPath)
		command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		command.Stderr = os.Stderr
		input, err := command.StdinPipe()
		if err != nil {
			return nil, err
		}
		output, err := command.StdoutPipe()
		if err != nil {
			input.Close()
			return nil, err
		}
		err = command.Start()
		if err != nil {
			input.Close()
			output.Close()
			return nil, fmt.Errorf("start deepagent-computer: %w", err)
		}
		desktop.command, desktop.input = command, input
		desktop.output = bufio.NewScanner(output)
		desktop.output.Buffer(make([]byte, 4096), 12<<20)
	}
	hasSideEffects := operation != "observe" && operation != "check" && operation != "ping" && operation != "release"
	requestCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	command := desktop.command
	canceled := make(chan struct{})
	stop := context.AfterFunc(requestCtx, func() { _ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL); close(canceled) })
	request := struct {
		Action
		Operation string `json:"operation"`
		Owner     string `json:"owner"`
	}{action, operation, ownerRunID}
	var result struct {
		Observation
		Error          string `json:"error"`
		OutcomeUnknown bool   `json:"outcome_unknown"`
	}
	err = json.NewEncoder(desktop.input).Encode(request)
	if err == nil {
		if desktop.output.Scan() {
			err = json.Unmarshal(desktop.output.Bytes(), &result)
			if err != nil {
				err = fmt.Errorf("invalid desktop reply: %w", err)
			}
		} else {
			err = desktop.output.Err()
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
		}
	}
	if !stop() {
		<-canceled
	}
	if requestCtx.Err() != nil {
		err = requestCtx.Err()
	}
	if err != nil {
		desktop.stopHelper()
		if hasSideEffects {
			err = errors.Join(ErrOutcomeUnknown, err)
		}
		return nil, err
	}
	if result.Error != "" {
		err = errors.New(result.Error)
		if result.OutcomeUnknown {
			err = errors.Join(ErrOutcomeUnknown, err)
		}
		return nil, err
	}
	return &result.Observation, nil
}

func (desktop *Desktop) stopHelper() {
	if desktop.command == nil {
		return
	}
	_ = syscall.Kill(-desktop.command.Process.Pid, syscall.SIGKILL)
	_ = desktop.input.Close()
	_ = desktop.command.Wait()
	desktop.command, desktop.input, desktop.output = nil, nil, nil
}

func (desktop *Desktop) Close(context.Context) error {
	desktop.mu.Lock()
	defer desktop.mu.Unlock()
	desktop.closed = true
	desktop.stopHelper()
	return nil
}
