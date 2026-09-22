package filesystem

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"eino-cli/deepagent/core/backends"
	executemw "eino-cli/deepagent/core/middlewares/execute"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

const maxShellJobs = 32

type shellJob struct {
	id       string
	started  time.Time
	output   bytes.Buffer
	done     bool
	exitCode int
	mu       sync.Mutex
}

var shellJobRegistry = struct {
	sync.Mutex
	seq  atomic.Uint64
	jobs map[string]*shellJob
}{jobs: map[string]*shellJob{}}

func newBackgroundShellTools(workspace backends.WorkspaceBackend, classifier executemw.CommandClassifier) []tool.BaseTool {
	shell, _ := utils.InferTool("shell", "Run a safe shell command; long-running commands continue in the background.", func(ctx context.Context, in struct {
		Command    string `json:"command" jsonschema:"required"`
		WorkingDir string `json:"working_directory,omitempty"`
		TimeoutMS  int    `json:"timeout_ms,omitempty"`
	}) (string, error) {
		command := strings.TrimSpace(in.Command)
		if command == "" {
			return "", fmt.Errorf("command is required")
		}
		workDir, err := workspacePath(workspace.RootDir(), in.WorkingDir)
		if err != nil {
			return "", err
		}
		classification, err := classifier.Classify(ctx, executemw.CommandSpec{Command: command, RawCommand: command, WorkDir: workDir})
		if err != nil {
			return "", err
		}
		if classification.Classification != executemw.ClassificationSafe {
			return "", fmt.Errorf("command denied (%s): %s", classification.Classification, classification.Reason)
		}
		job, done, err := startShellJob(command, workDir)
		if err != nil {
			return "", err
		}
		timeout := time.Duration(in.TimeoutMS) * time.Millisecond
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		select {
		case <-done:
			return formatShellSnapshot(job, 0), nil
		case <-time.After(timeout):
			return fmt.Sprintf("Command is still running in background. task_id=%s", job.id), nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})

	await, _ := utils.InferTool("await_shell", "Wait for a background shell task or an output pattern.", func(ctx context.Context, in struct {
		TaskID      string `json:"task_id" jsonschema:"required"`
		TimeoutMS   int    `json:"timeout_ms,omitempty"`
		Pattern     string `json:"pattern,omitempty"`
		SinceOffset int    `json:"since_offset,omitempty"`
	}) (string, error) {
		job := findShellJob(in.TaskID)
		if job == nil {
			return "", fmt.Errorf("unknown task_id: %s", in.TaskID)
		}
		var pattern *regexp.Regexp
		if in.Pattern != "" {
			var err error
			pattern, err = regexp.Compile(in.Pattern)
			if err != nil {
				return "", err
			}
		}
		timeout := time.Duration(in.TimeoutMS) * time.Millisecond
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		deadline := time.NewTimer(timeout)
		ticker := time.NewTicker(50 * time.Millisecond)
		defer deadline.Stop()
		defer ticker.Stop()
		for {
			output, done, _ := job.snapshot()
			if done || (pattern != nil && pattern.MatchString(output)) {
				return formatShellSnapshot(job, in.SinceOffset), nil
			}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-deadline.C:
				return formatShellSnapshot(job, in.SinceOffset), nil
			case <-ticker.C:
			}
		}
	})
	return []tool.BaseTool{shell, await}
}

func startShellJob(command, workDir string) (*shellJob, <-chan struct{}, error) {
	cmd := exec.Command("/bin/bash", "-lc", command)
	cmd.Dir = workDir
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	job := &shellJob{id: fmt.Sprintf("shell-%d", shellJobRegistry.seq.Add(1)), started: time.Now()}
	shellJobRegistry.Lock()
	shellJobRegistry.jobs[job.id] = job
	pruneShellJobs()
	shellJobRegistry.Unlock()
	done := make(chan struct{})
	go copyShellOutput(job, stdout)
	go copyShellOutput(job, stderr)
	go func() {
		err := cmd.Wait()
		exitCode := 0
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else if err != nil {
			exitCode = 1
			job.append([]byte(err.Error()))
		}
		job.mu.Lock()
		job.done, job.exitCode = true, exitCode
		job.mu.Unlock()
		close(done)
	}()
	return job, done, nil
}

func copyShellOutput(job *shellJob, reader io.Reader) {
	buffer := make([]byte, 4096)
	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			job.append(buffer[:n])
		}
		if err != nil {
			return
		}
	}
}

func (j *shellJob) append(data []byte) {
	j.mu.Lock()
	j.output.Write(data)
	j.mu.Unlock()
}

func (j *shellJob) snapshot() (string, bool, int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.output.String(), j.done, j.exitCode
}

func findShellJob(id string) *shellJob {
	shellJobRegistry.Lock()
	defer shellJobRegistry.Unlock()
	return shellJobRegistry.jobs[id]
}

func pruneShellJobs() {
	for len(shellJobRegistry.jobs) > maxShellJobs {
		var oldest *shellJob
		for _, job := range shellJobRegistry.jobs {
			_, done, _ := job.snapshot()
			if done && (oldest == nil || job.started.Before(oldest.started)) {
				oldest = job
			}
		}
		if oldest == nil {
			return
		}
		delete(shellJobRegistry.jobs, oldest.id)
	}
}

func formatShellSnapshot(job *shellJob, offset int) string {
	output, done, exitCode := job.snapshot()
	if offset < 0 {
		offset = 0
	}
	if offset > len(output) {
		offset = len(output)
	}
	status := "running"
	if done {
		status = fmt.Sprintf("done exit_code=%d", exitCode)
	}
	body := limitBytes(output[offset:], 64*1024)
	if strings.TrimSpace(body) == "" {
		body = "[no output]"
	}
	return fmt.Sprintf("task_id=%s status=%s output_offset=%d\n%s", job.id, status, len(output), body)
}
