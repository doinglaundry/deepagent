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
	workspace := mustLocalFilesystem(t, &LocalFilesystemConfig{RootDir: t.TempDir(), VirtualMode: true})
	first := NewCommands("thread-1", workspace)
	second := NewCommands("thread-2", workspace)
	defer first.Close(context.Background())
	defer second.Close(context.Background())
	id, err := first.Start(ctx, CommandRequest{Command: "printf ready; sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := first.Wait(ctx, id, "ready", 0)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Done || snapshot.Output != "ready" {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	_, waitErr := second.Wait(ctx, id, "", 0)
	if waitErr == nil {
		t.Fatal("another thread read the job")
	}
	secondCancelErr := second.Cancel(ctx, id)
	if secondCancelErr == nil {
		t.Fatal("another thread canceled the job")
	}
	cancelErr := first.Cancel(ctx, id)
	if cancelErr != nil {
		t.Fatal(cancelErr)
	}
	snapshot, err = first.Wait(ctx, id, "", 0)
	if err != nil || !snapshot.Done {
		t.Fatalf("job did not stop: %+v %v", snapshot, err)
	}
}
func TestCommands_CloseKillsJobsAndBoundsOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	service := NewCommands("thread", mustLocalFilesystem(t, &LocalFilesystemConfig{RootDir: t.TempDir(), VirtualMode: true}))
	result, err := service.Execute(ctx, CommandRequest{Command: "printf 1234567890", MaxOutputBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "7890" || !result.Truncated {
		t.Fatalf("unbounded output: %+v", result)
	}
	id, err := service.Start(ctx, CommandRequest{Command: "printf ready; sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	_, waitErr := service.Wait(ctx, id, "ready", 0)
	if waitErr != nil {
		t.Fatal(waitErr)
	}
	closeErr := service.Close(ctx)
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	_, startErr := service.Start(ctx, CommandRequest{Command: "true"})
	if startErr == nil {
		t.Fatal("closed service started a job")
	}
	snapshot, err := service.Wait(ctx, id, "", 0)
	if err != nil || !snapshot.Done {
		t.Fatalf("job survived close: %+v %v", snapshot, err)
	}
}

func TestCommandsPrefixRetentionAndIncrementalOffsets(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	service := NewCommands("thread", mustLocalFilesystem(t, &LocalFilesystemConfig{RootDir: t.TempDir(), VirtualMode: true}))
	defer service.Close(context.Background())
	id, err := service.Start(ctx, CommandRequest{Command: "printf 1234567890", MaxOutputBytes: 4, KeepOutputPrefix: true})
	if err != nil {
		t.Fatal(err)
	}
	// Wait until completion before reading: a slow consumer must still get the prefix.
	first, err := service.Wait(ctx, id, "", 0)
	if err != nil || first.Output != "1234" || first.Offset != 10 || !first.Truncated {
		t.Fatalf("prefix snapshot=%+v err=%v", first, err)
	}
	next, err := service.Wait(ctx, id, "", first.Offset)
	if err != nil || next.Output != "" || next.Offset != 10 {
		t.Fatalf("duplicate output=%+v err=%v", next, err)
	}
	partial, err := service.Wait(ctx, id, "", 2)
	if err != nil || partial.Output != "34" {
		t.Fatalf("partial prefix=%+v err=%v", partial, err)
	}
}

func TestDockerFilesystemCloseTimeoutRetainsContainerUntilJobsFinish(t *testing.T) {
	released := 0
	job := &commandJob{done: make(chan struct{}), cancel: func() {}}
	filesystem := &DockerFilesystem{
		commands:         &Commands{jobs: map[string]*commandJob{"running": job}},
		releaseContainer: func() { released++ },
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := filesystem.Close(ctx)
	if !errors.Is(err, context.Canceled) || released != 0 {
		t.Fatalf("released container before job completion: error=%v releases=%d", err, released)
	}
	close(job.done)
	err = filesystem.Close(context.Background())
	if err != nil || released != 1 {
		t.Fatalf("retry did not release the container: error=%v releases=%d", err, released)
	}
}
