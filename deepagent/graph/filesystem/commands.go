package filesystem

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	agentmodel "eino-cli/deepagent/model"

	"github.com/google/uuid"
)

// Commands owns jobs for exactly one thread. There is no process-global job map.
type Commands struct {
	mu           sync.Mutex
	threadID     string
	filesystem   agentmodel.Filesystem
	buildCommand func(context.Context, agentmodel.CommandRequest, string, string) (*exec.Cmd, error)
	killRemote   func(context.Context, string) error
	jobsByID     map[string]*commandJob
	closed       bool
}
type commandJob struct {
	mu             sync.Mutex
	id             string
	threadID       string
	output         []byte
	total          int
	limit          int
	keepPrefix     bool
	exitCode       int
	timedOut       bool
	done           chan struct{}
	changed        chan struct{}
	cancel         context.CancelFunc
	stopRemoteOnce sync.Once
	stopRemoteErr  error
	finished       bool
	started        time.Time
}

func NewCommands(threadID string, filesystem agentmodel.Filesystem) *Commands {
	return &Commands{threadID: threadID, filesystem: filesystem, jobsByID: map[string]*commandJob{}}
}

// NewDockerCommands runs every job in the named container. The job ledger is
// still thread scoped and shared by execute, shell and await_shell.
func NewDockerCommands(threadID string, filesystem agentmodel.Filesystem, containerID string) *Commands {
	commands := NewCommands(threadID, filesystem)
	commands.buildCommand = func(ctx context.Context, request agentmodel.CommandRequest, workDir, jobID string) (*exec.Cmd, error) {
		if containerID == "" {
			return nil, fmt.Errorf("docker container ID is required")
		}
		dockerArgs := []string{"exec", "-i", "-w", workDir}
		for _, pair := range buildEnvPairs(request.Env) {
			dockerArgs = append(dockerArgs, "-e", pair)
		}
		pidFile := "/tmp/deepagent-command-" + jobID + ".pid"
		const script = `marker=$1; shift; printf '%s' "$$" > "$marker"; /bin/sh -c "$1"; code=$?; rm -f "$marker"; exit "$code"`
		dockerArgs = append(dockerArgs, containerID, "/bin/sh", "-c", script, "sh", pidFile, request.Command)
		return exec.CommandContext(ctx, "docker", dockerArgs...), nil
	}
	commands.killRemote = func(ctx context.Context, jobID string) error {
		pidFile := "/tmp/deepagent-command-" + jobID + ".pid"
		const script = `marker=$1; n=0; while [ ! -s "$marker" ] && [ "$n" -lt 40 ]; do sleep .05; n=$((n+1)); done; [ -s "$marker" ] || exit 0; pid=$(cat "$marker"); case "$pid" in ''|*[!0-9]*) exit 2;; esac; kill_tree() { for status in /proc/[0-9]*/status; do [ -r "$status" ] || continue; while read -r key value rest; do [ "$key" = PPid: ] && break; done < "$status"; if [ "$value" = "$1" ]; then child=${status#/proc/}; child=${child%/status}; kill_tree "$child"; fi; done; kill -KILL "$1" 2>/dev/null || true; }; kill_tree "$pid"; rm -f "$marker"`
		shellCommand := exec.CommandContext(ctx, "docker", "exec", containerID, "/bin/sh", "-c", script, "sh", pidFile)
		commandOutput, err := shellCommand.CombinedOutput()
		if err != nil {
			return fmt.Errorf("stop Docker job %s: %s: %w", jobID, strings.TrimSpace(string(commandOutput)), err)
		}
		return nil
	}
	return commands
}

func (commands *Commands) stopRemoteJob(commandJobRecord *commandJob) error {
	if commands.killRemote == nil {
		return nil
	}
	commandJobRecord.stopRemoteOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		commandJobRecord.stopRemoteErr = commands.killRemote(ctx, commandJobRecord.id)
	})
	return commandJobRecord.stopRemoteErr
}
func (commands *Commands) Start(ctx context.Context, request agentmodel.CommandRequest) (string, error) {
	if commands.filesystem == nil || commands.threadID == "" {
		return "", fmt.Errorf("thread and workspace are required")
	}
	contextErr := ctx.Err()
	if contextErr != nil {
		return "", contextErr
	}
	if strings.TrimSpace(request.Command) == "" {
		return "", fmt.Errorf("command is required")
	}
	workDir := request.WorkDir
	if workDir == "" {
		workDir = "."
	}
	workDir, err := commands.filesystem.Resolve(ctx, workDir, false)
	if err != nil {
		return "", err
	}
	if commands.buildCommand == nil {
		directoryInfo, err := os.Stat(workDir)
		if err != nil {
			return "", err
		}
		if !directoryInfo.IsDir() {
			return "", fmt.Errorf("command working directory is not a directory")
		}
	} else {
		_, err := commands.filesystem.List(ctx, workDir)
		if err != nil {
			return "", fmt.Errorf("command working directory: %w", err)
		}
	}
	var jobContext context.Context
	var cancel context.CancelFunc
	if request.Timeout > 0 {
		jobContext, cancel = context.WithTimeout(ctx, request.Timeout)
	} else {
		jobContext, cancel = context.WithCancel(ctx)
	}
	outputLimit := request.MaxOutputBytes
	if outputLimit <= 0 {
		outputLimit = 64 << 10
	}
	commandJobRecord := &commandJob{id: uuid.NewString(), threadID: commands.threadID, limit: outputLimit, keepPrefix: request.KeepOutputPrefix, done: make(chan struct{}), changed: make(chan struct{}), cancel: cancel, started: time.Now()}
	var shellCommand *exec.Cmd
	if commands.buildCommand != nil {
		shellCommand, err = commands.buildCommand(jobContext, request, workDir, commandJobRecord.id)
		if err != nil {
			cancel()
			return "", err
		}
	} else {
		shellCommand = exec.CommandContext(jobContext, "/bin/bash", "-c", request.Command)
		shellCommand.Dir = workDir
		shellCommand.Env = append(os.Environ(), buildEnvPairs(request.Env)...)
	}
	shellCommand.Stdout = commandJobRecord
	shellCommand.Stderr = commandJobRecord
	configureShellCommandCancel(shellCommand)
	commands.mu.Lock()
	if commands.closed {
		commands.mu.Unlock()
		cancel()
		return "", fmt.Errorf("command service closed")
	}
	if len(commands.jobsByID) >= 32 {
		var oldestJob *commandJob
		for _, candidateJob := range commands.jobsByID {
			candidateJob.mu.Lock()
			finished := candidateJob.finished
			candidateJob.mu.Unlock()
			if finished && (oldestJob == nil || candidateJob.started.Before(oldestJob.started)) {
				oldestJob = candidateJob
			}
		}
		if oldestJob != nil {
			delete(commands.jobsByID, oldestJob.id)
		} else {
			commands.mu.Unlock()
			cancel()
			return "", fmt.Errorf("thread has too many running commands")
		}
	}
	startErr := shellCommand.Start()
	if startErr != nil {
		commands.mu.Unlock()
		cancel()
		return "", startErr
	}
	commands.jobsByID[commandJobRecord.id] = commandJobRecord
	commands.mu.Unlock()
	go func() {
		err := shellCommand.Wait()
		if jobContext.Err() != nil {
			_ = commands.stopRemoteJob(commandJobRecord)
		}
		exitCode := 0
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = -1
			}
		}
		commandJobRecord.mu.Lock()
		commandJobRecord.exitCode = exitCode
		commandJobRecord.timedOut = errors.Is(jobContext.Err(), context.DeadlineExceeded)
		commandJobRecord.finished = true
		close(commandJobRecord.changed)
		close(commandJobRecord.done)
		commandJobRecord.mu.Unlock()
		cancel()
	}()
	return commandJobRecord.id, nil
}
func (commandJob *commandJob) Write(data []byte) (int, error) {
	commandJob.mu.Lock()
	defer commandJob.mu.Unlock()
	byteCount := len(data)
	commandJob.total += byteCount
	if commandJob.keepPrefix {
		remainingCapacity := commandJob.limit - len(commandJob.output)
		commandJob.output = append(commandJob.output, data[:min(byteCount, remainingCapacity)]...)
	} else if byteCount >= commandJob.limit {
		commandJob.output = append(commandJob.output[:0], data[byteCount-commandJob.limit:]...)
	} else {
		commandJob.output = append(commandJob.output, data...)
		if len(commandJob.output) > commandJob.limit {
			commandJob.output = append(commandJob.output[:0], commandJob.output[len(commandJob.output)-commandJob.limit:]...)
		}
	}
	close(commandJob.changed)
	commandJob.changed = make(chan struct{})
	return byteCount, nil
}
func (commands *Commands) findJob(jobID string) (*commandJob, error) {
	commands.mu.Lock()
	defer commands.mu.Unlock()
	commandJobRecord, ok := commands.jobsByID[jobID]
	if !ok {
		return nil, fmt.Errorf("unknown task_id for thread %s: %s", commands.threadID, jobID)
	}
	return commandJobRecord, nil
}
func (commandJob *commandJob) buildSnapshot(offset int) (*agentmodel.CommandSnapshot, <-chan struct{}, error) {
	commandJob.mu.Lock()
	defer commandJob.mu.Unlock()
	if offset < 0 || offset > commandJob.total {
		return nil, nil, fmt.Errorf("invalid output offset %d", offset)
	}
	outputStart := offset - (commandJob.total - len(commandJob.output))
	truncated := offset < commandJob.total-len(commandJob.output)
	if commandJob.keepPrefix {
		outputStart = min(offset, len(commandJob.output))
		truncated = commandJob.total > len(commandJob.output)
	}
	if outputStart < 0 {
		outputStart = 0
	}
	return &agentmodel.CommandSnapshot{ID: commandJob.id, ThreadID: commandJob.threadID, Output: string(commandJob.output[outputStart:]), ExitCode: commandJob.exitCode, Done: commandJob.finished, Offset: commandJob.total, Truncated: truncated, TimedOut: commandJob.timedOut}, commandJob.changed, nil
}
func (commands *Commands) Wait(ctx context.Context, jobID, pattern string, offset int) (*agentmodel.CommandSnapshot, error) {
	commandJobRecord, err := commands.findJob(jobID)
	if err != nil {
		return nil, err
	}
	var patternRegexp *regexp.Regexp
	if pattern != "" {
		patternRegexp, err = regexp.Compile(pattern)
		if err != nil {
			return nil, err
		}
	}
	for {
		commandSnapshot, changed, err := commandJobRecord.buildSnapshot(offset)
		if err != nil {
			return nil, err
		}
		if commandSnapshot.Done || (patternRegexp != nil && patternRegexp.MatchString(commandSnapshot.Output)) {
			return commandSnapshot, nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return commandSnapshot, ctx.Err()
		}
	}
}

// stop requests termination without waiting, so Close can stop every job before joining them.
func (commands *Commands) stopJob(commandJobRecord *commandJob) error {
	commandJobRecord.mu.Lock()
	finished := commandJobRecord.finished
	commandJobRecord.mu.Unlock()
	var stopErr error
	if !finished {
		stopErr = commands.stopRemoteJob(commandJobRecord)
	}
	commandJobRecord.cancel()
	return stopErr
}

func (commands *Commands) Cancel(ctx context.Context, jobID string) error {
	commandJobRecord, err := commands.findJob(jobID)
	if err != nil {
		return err
	}
	stopErr := commands.stopJob(commandJobRecord)
	select {
	case <-commandJobRecord.done:
		return stopErr
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (commands *Commands) Close(ctx context.Context) error {
	commands.mu.Lock()
	commands.closed = true
	jobs := make([]*commandJob, 0, len(commands.jobsByID))
	for _, commandJobRecord := range commands.jobsByID {
		jobs = append(jobs, commandJobRecord)
	}
	commands.mu.Unlock()
	var stopErr error
	for _, commandJobRecord := range jobs {
		stopErr = errors.Join(stopErr, commands.stopJob(commandJobRecord))
	}
	for _, commandJobRecord := range jobs {
		select {
		case <-commandJobRecord.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return stopErr
}
func (commands *Commands) Execute(ctx context.Context, request agentmodel.CommandRequest) (*agentmodel.CommandResult, error) {
	jobID, err := commands.Start(ctx, request)
	if err != nil {
		return nil, err
	}
	commandSnapshot, err := commands.Wait(ctx, jobID, "", 0)
	if err != nil {
		_ = commands.Cancel(context.Background(), jobID)
		return nil, err
	}
	return &agentmodel.CommandResult{Output: commandSnapshot.Output, ExitCode: commandSnapshot.ExitCode, TimedOut: commandSnapshot.TimedOut, Truncated: commandSnapshot.Truncated, ShellSessionID: jobID}, nil
}

func buildEnvPairs(env map[string]string) []string {
	pairs := make([]string, 0, len(env))
	for variableName, value := range env {
		pairs = append(pairs, variableName+"="+value)
	}
	return pairs
}

var _ agentmodel.CommandService = (*Commands)(nil)
