package backend

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

	"github.com/google/uuid"
)

type CommandService interface {
	Execute(context.Context, CommandRequest) (*CommandResult, error)
	Start(context.Context, CommandRequest) (string, error)
	Wait(context.Context, string, string, int) (*CommandSnapshot, error)
	Cancel(context.Context, string) error
	Close(context.Context) error
}
type CommandSnapshot struct {
	ID        string
	ThreadID  string
	Output    string
	ExitCode  int
	Done      bool
	Offset    int
	Truncated bool
	TimedOut  bool
}

// Commands owns jobs for exactly one thread. There is no process-global job map.
type Commands struct {
	mu         sync.Mutex
	threadID   string
	workspace  Filesystem
	command    func(context.Context, CommandRequest, string, string) (*exec.Cmd, error)
	killRemote func(context.Context, string) error
	jobs       map[string]*commandJob
	closed     bool
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

func NewCommands(threadID string, workspace Filesystem) *Commands {
	return &Commands{threadID: threadID, workspace: workspace, jobs: map[string]*commandJob{}}
}

// NewDockerCommands runs every job in the named container. The job ledger is
// still thread scoped and shared by execute, shell and await_shell.
func NewDockerCommands(threadID string, workspace Filesystem, containerID string) *Commands {
	s := NewCommands(threadID, workspace)
	s.command = func(ctx context.Context, request CommandRequest, dir, jobID string) (*exec.Cmd, error) {
		if containerID == "" {
			return nil, fmt.Errorf("docker container ID is required")
		}
		args := []string{"exec", "-i", "-w", dir}
		for _, pair := range envPairs(request.Env) {
			args = append(args, "-e", pair)
		}
		marker := "/tmp/deepagent-command-" + jobID + ".pid"
		const script = `marker=$1; shift; printf '%s' "$$" > "$marker"; /bin/sh -c "$1"; code=$?; rm -f "$marker"; exit "$code"`
		args = append(args, containerID, "/bin/sh", "-c", script, "sh", marker, request.Command)
		return exec.CommandContext(ctx, "docker", args...), nil
	}
	s.killRemote = func(ctx context.Context, jobID string) error {
		marker := "/tmp/deepagent-command-" + jobID + ".pid"
		const script = `marker=$1; n=0; while [ ! -s "$marker" ] && [ "$n" -lt 40 ]; do sleep .05; n=$((n+1)); done; [ -s "$marker" ] || exit 0; pid=$(cat "$marker"); case "$pid" in ''|*[!0-9]*) exit 2;; esac; kill_tree() { for status in /proc/[0-9]*/status; do [ -r "$status" ] || continue; while read -r key value rest; do [ "$key" = PPid: ] && break; done < "$status"; if [ "$value" = "$1" ]; then child=${status#/proc/}; child=${child%/status}; kill_tree "$child"; fi; done; kill -KILL "$1" 2>/dev/null || true; }; kill_tree "$pid"; rm -f "$marker"`
		cmd := exec.CommandContext(ctx, "docker", "exec", containerID, "/bin/sh", "-c", script, "sh", marker)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("stop Docker job %s: %s: %w", jobID, strings.TrimSpace(string(out)), err)
		}
		return nil
	}
	return s
}

func (s *Commands) stopRemote(job *commandJob) error {
	if s.killRemote == nil {
		return nil
	}
	job.stopRemoteOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		job.stopRemoteErr = s.killRemote(ctx, job.id)
	})
	return job.stopRemoteErr
}
func (s *Commands) Start(ctx context.Context, request CommandRequest) (string, error) {
	if s.workspace == nil || s.threadID == "" {
		return "", fmt.Errorf("thread and workspace are required")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if strings.TrimSpace(request.Command) == "" {
		return "", fmt.Errorf("command is required")
	}
	dir := request.WorkDir
	if dir == "" {
		dir = "."
	}
	dir, err := s.workspace.Resolve(ctx, dir, false)
	if err != nil {
		return "", err
	}
	if s.command == nil {
		info, err := os.Stat(dir)
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			return "", fmt.Errorf("command working directory is not a directory")
		}
	} else if _, err := s.workspace.List(ctx, dir); err != nil {
		return "", fmt.Errorf("command working directory: %w", err)
	}
	jobCtx, cancel := context.WithCancel(ctx)
	if request.Timeout > 0 {
		var timeoutCancel context.CancelFunc
		jobCtx, timeoutCancel = context.WithTimeout(jobCtx, request.Timeout)
		originalCancel := cancel
		cancel = func() { timeoutCancel(); originalCancel() }
	}
	limit := request.MaxOutputBytes
	if limit <= 0 {
		limit = 64 << 10
	}
	job := &commandJob{id: uuid.NewString(), threadID: s.threadID, limit: limit, keepPrefix: request.KeepOutputPrefix, done: make(chan struct{}), changed: make(chan struct{}), cancel: cancel, started: time.Now()}
	var cmd *exec.Cmd
	if s.command != nil {
		cmd, err = s.command(jobCtx, request, dir, job.id)
		if err != nil {
			cancel()
			return "", err
		}
	} else {
		cmd = exec.CommandContext(jobCtx, "/bin/bash", "-c", request.Command)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), envPairs(request.Env)...)
	}
	cmd.Stdout = job
	cmd.Stderr = job
	configureShellCommandCancel(cmd)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel()
		return "", fmt.Errorf("command service closed")
	}
	if len(s.jobs) >= 32 {
		var oldest *commandJob
		for _, candidate := range s.jobs {
			candidate.mu.Lock()
			finished := candidate.finished
			candidate.mu.Unlock()
			if finished && (oldest == nil || candidate.started.Before(oldest.started)) {
				oldest = candidate
			}
		}
		if oldest != nil {
			delete(s.jobs, oldest.id)
		} else {
			s.mu.Unlock()
			cancel()
			return "", fmt.Errorf("thread has too many running commands")
		}
	}
	if err := cmd.Start(); err != nil {
		s.mu.Unlock()
		cancel()
		return "", err
	}
	s.jobs[job.id] = job
	s.mu.Unlock()
	go func() {
		err := cmd.Wait()
		if jobCtx.Err() != nil {
			_ = s.stopRemote(job)
		}
		code := 0
		if err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				code = exit.ExitCode()
			} else {
				code = -1
			}
		}
		job.mu.Lock()
		job.exitCode = code
		job.timedOut = errors.Is(jobCtx.Err(), context.DeadlineExceeded)
		job.finished = true
		close(job.changed)
		close(job.done)
		job.mu.Unlock()
		cancel()
	}()
	return job.id, nil
}
func (j *commandJob) Write(data []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	n := len(data)
	j.total += n
	if j.keepPrefix {
		remaining := j.limit - len(j.output)
		j.output = append(j.output, data[:min(n, remaining)]...)
	} else if n >= j.limit {
		j.output = append(j.output[:0], data[n-j.limit:]...)
	} else {
		j.output = append(j.output, data...)
		if len(j.output) > j.limit {
			j.output = append(j.output[:0], j.output[len(j.output)-j.limit:]...)
		}
	}
	close(j.changed)
	j.changed = make(chan struct{})
	return n, nil
}
func (s *Commands) find(id string) (*commandJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		return nil, fmt.Errorf("unknown task_id for thread %s: %s", s.threadID, id)
	}
	return job, nil
}
func (j *commandJob) snapshot(offset int) (*CommandSnapshot, <-chan struct{}, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if offset < 0 || offset > j.total {
		return nil, nil, fmt.Errorf("invalid output offset %d", offset)
	}
	start := offset - (j.total - len(j.output))
	truncated := offset < j.total-len(j.output)
	if j.keepPrefix {
		start = min(offset, len(j.output))
		truncated = j.total > len(j.output)
	}
	if start < 0 {
		start = 0
	}
	return &CommandSnapshot{ID: j.id, ThreadID: j.threadID, Output: string(j.output[start:]), ExitCode: j.exitCode, Done: j.finished, Offset: j.total, Truncated: truncated, TimedOut: j.timedOut}, j.changed, nil
}
func (s *Commands) Wait(ctx context.Context, id, pattern string, offset int) (*CommandSnapshot, error) {
	job, err := s.find(id)
	if err != nil {
		return nil, err
	}
	var re *regexp.Regexp
	if pattern != "" {
		re, err = regexp.Compile(pattern)
		if err != nil {
			return nil, err
		}
	}
	for {
		snapshot, changed, err := job.snapshot(offset)
		if err != nil {
			return nil, err
		}
		if snapshot.Done || (re != nil && re.MatchString(snapshot.Output)) {
			return snapshot, nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return snapshot, ctx.Err()
		}
	}
}
func (s *Commands) Cancel(ctx context.Context, id string) error {
	job, err := s.find(id)
	if err != nil {
		return err
	}
	job.mu.Lock()
	finished := job.finished
	job.mu.Unlock()
	var stopErr error
	if !finished {
		stopErr = s.stopRemote(job)
	}
	job.cancel()
	select {
	case <-job.done:
		return stopErr
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *Commands) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	jobs := make([]*commandJob, 0, len(s.jobs))
	for _, job := range s.jobs {
		jobs = append(jobs, job)
	}
	s.mu.Unlock()
	var stopErr error
	for _, job := range jobs {
		job.mu.Lock()
		finished := job.finished
		job.mu.Unlock()
		if !finished {
			stopErr = errors.Join(stopErr, s.stopRemote(job))
		}
		job.cancel()
	}
	for _, job := range jobs {
		select {
		case <-job.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return stopErr
}
func (s *Commands) Execute(ctx context.Context, request CommandRequest) (*CommandResult, error) {
	id, err := s.Start(ctx, request)
	if err != nil {
		return nil, err
	}
	snapshot, err := s.Wait(ctx, id, "", 0)
	if err != nil {
		_ = s.Cancel(context.Background(), id)
		return nil, err
	}
	return &CommandResult{Output: snapshot.Output, ExitCode: snapshot.ExitCode, TimedOut: snapshot.TimedOut, Truncated: snapshot.Truncated, ShellSessionID: id}, nil
}

var _ CommandService = (*Commands)(nil)
