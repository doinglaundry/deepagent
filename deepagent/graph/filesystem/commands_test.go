package filesystem

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestShell_JobsAreThreadScoped(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	localFilesystem := newTestLocalFilesystem(t, &LocalFilesystemConfig{RootDir: t.TempDir(), VirtualMode: true})
	firstCommands := NewCommands("thread-1", localFilesystem)
	secondCommands := NewCommands("thread-2", localFilesystem)
	defer firstCommands.Close(context.Background())
	defer secondCommands.Close(context.Background())
	jobID, err := firstCommands.Start(ctx, CommandRequest{Command: "printf ready; sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	commandSnapshot, err := firstCommands.Wait(ctx, jobID, "ready", 0)
	if err != nil {
		t.Fatal(err)
	}
	if commandSnapshot.Done || commandSnapshot.Output != "ready" {
		t.Fatalf("snapshot=%+v", commandSnapshot)
	}
	_, waitErr := secondCommands.Wait(ctx, jobID, "", 0)
	if waitErr == nil {
		t.Fatal("another thread read the job")
	}
	secondCancelErr := secondCommands.Cancel(ctx, jobID)
	if secondCancelErr == nil {
		t.Fatal("another thread canceled the job")
	}
	cancelErr := firstCommands.Cancel(ctx, jobID)
	if cancelErr != nil {
		t.Fatal(cancelErr)
	}
	commandSnapshot, err = firstCommands.Wait(ctx, jobID, "", 0)
	if err != nil || !commandSnapshot.Done {
		t.Fatalf("job did not stop: %+v %v", commandSnapshot, err)
	}
}
func TestCommands_CloseKillsJobsAndBoundsOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	commands := NewCommands("thread", newTestLocalFilesystem(t, &LocalFilesystemConfig{RootDir: t.TempDir(), VirtualMode: true}))
	result, err := commands.Execute(ctx, CommandRequest{Command: "printf 1234567890", MaxOutputBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "7890" || !result.Truncated {
		t.Fatalf("unbounded output: %+v", result)
	}
	jobID, err := commands.Start(ctx, CommandRequest{Command: "printf ready; sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	_, waitErr := commands.Wait(ctx, jobID, "ready", 0)
	if waitErr != nil {
		t.Fatal(waitErr)
	}
	closeErr := commands.Close(ctx)
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	_, startErr := commands.Start(ctx, CommandRequest{Command: "true"})
	if startErr == nil {
		t.Fatal("closed service started a job")
	}
	commandSnapshot, err := commands.Wait(ctx, jobID, "", 0)
	if err != nil || !commandSnapshot.Done {
		t.Fatalf("job survived close: %+v %v", commandSnapshot, err)
	}
}

func TestCommandsPrefixRetentionAndIncrementalOffsets(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	commands := NewCommands("thread", newTestLocalFilesystem(t, &LocalFilesystemConfig{RootDir: t.TempDir(), VirtualMode: true}))
	defer commands.Close(context.Background())
	jobID, err := commands.Start(ctx, CommandRequest{Command: "printf 1234567890", MaxOutputBytes: 4, KeepOutputPrefix: true})
	if err != nil {
		t.Fatal(err)
	}
	// Wait until completion before reading: a slow consumer must still get the prefix.
	firstSnapshot, err := commands.Wait(ctx, jobID, "", 0)
	if err != nil || firstSnapshot.Output != "1234" || firstSnapshot.Offset != 10 || !firstSnapshot.Truncated {
		t.Fatalf("prefix snapshot=%+v err=%v", firstSnapshot, err)
	}
	nextSnapshot, err := commands.Wait(ctx, jobID, "", firstSnapshot.Offset)
	if err != nil || nextSnapshot.Output != "" || nextSnapshot.Offset != 10 {
		t.Fatalf("duplicate output=%+v err=%v", nextSnapshot, err)
	}
	partialSnapshot, err := commands.Wait(ctx, jobID, "", 2)
	if err != nil || partialSnapshot.Output != "34" {
		t.Fatalf("partial prefix=%+v err=%v", partialSnapshot, err)
	}
}

func TestDockerFilesystemCloseTimeoutRetainsContainerUntilJobsFinish(t *testing.T) {
	releaseCount := 0
	runningJob := &commandJob{done: make(chan struct{}), cancel: func() {}}
	dockerFilesystem := &DockerFilesystem{
		commands:         &Commands{jobsByID: map[string]*commandJob{"running": runningJob}},
		releaseContainer: func() { releaseCount++ },
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := dockerFilesystem.Close(ctx)
	if !errors.Is(err, context.Canceled) || releaseCount != 0 {
		t.Fatalf("released container before job completion: error=%v releases=%d", err, releaseCount)
	}
	close(runningJob.done)
	err = dockerFilesystem.Close(context.Background())
	if err != nil || releaseCount != 1 {
		t.Fatalf("retry did not release the container: error=%v releases=%d", err, releaseCount)
	}
}
