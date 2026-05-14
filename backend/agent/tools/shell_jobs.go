package tools

import (
	"bytes"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

const maxShellJobs = 32

type shellJob struct {
	ID         string
	Command    string
	WorkingDir string
	StartedAt  time.Time
	Done       bool
	ExitCode   int
	Output     bytes.Buffer
	Process    *exec.Cmd
}

var shellJobs = struct {
	sync.Mutex
	seq  int
	jobs map[string]*shellJob
}{
	jobs: map[string]*shellJob{},
}

func addShellJob(command, workingDir string, cmd *exec.Cmd) *shellJob {
	shellJobs.Lock()
	defer shellJobs.Unlock()
	shellJobs.seq++
	id := fmt.Sprintf("shell-%d", shellJobs.seq)
	job := &shellJob{
		ID:         id,
		Command:    command,
		WorkingDir: workingDir,
		StartedAt:  time.Now(),
		Process:    cmd,
	}
	shellJobs.jobs[id] = job
	pruneShellJobsLocked()
	return job
}

func getShellJob(id string) (*shellJob, bool) {
	shellJobs.Lock()
	defer shellJobs.Unlock()
	job, ok := shellJobs.jobs[id]
	return job, ok
}

func appendShellOutput(job *shellJob, chunk []byte) {
	shellJobs.Lock()
	defer shellJobs.Unlock()
	job.Output.Write(chunk)
}

func finishShellJob(job *shellJob, exitCode int) {
	shellJobs.Lock()
	defer shellJobs.Unlock()
	job.Done = true
	job.ExitCode = exitCode
	pruneShellJobsLocked()
}

func snapshotShellJob(job *shellJob) (string, bool, int) {
	shellJobs.Lock()
	defer shellJobs.Unlock()
	return job.Output.String(), job.Done, job.ExitCode
}

func pruneShellJobsLocked() {
	if len(shellJobs.jobs) <= maxShellJobs {
		return
	}
	var oldestID string
	var oldestTime time.Time
	for id, job := range shellJobs.jobs {
		if !job.Done {
			continue
		}
		if oldestID == "" || job.StartedAt.Before(oldestTime) {
			oldestID = id
			oldestTime = job.StartedAt
		}
	}
	if oldestID != "" {
		delete(shellJobs.jobs, oldestID)
	}
}
