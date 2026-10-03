package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	filesystempkg "eino-cli/deepagent/graph/filesystem"

	einotool "github.com/cloudwego/eino/components/tool"
)

type joinedCommands struct {
	filesystempkg.CommandService
	joined chan error
}

// completedOutputCommands makes the first observation happen after process exit.
type completedOutputCommands struct{ filesystempkg.CommandService }

func (completedCommands completedOutputCommands) Wait(ctx context.Context, taskID, _ string, offset int) (*filesystempkg.CommandSnapshot, error) {
	return completedCommands.CommandService.Wait(ctx, taskID, "", offset)
}

func TestExecuteSlowConsumerRetainsFirstMiB(t *testing.T) {
	filesystem := newTestLocalFilesystem(t, &filesystempkg.LocalFilesystemConfig{RootDir: t.TempDir(), VirtualMode: true})
	commandService := filesystempkg.NewCommands("thread", filesystem)
	defer commandService.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	arguments, err := json.Marshal(map[string]string{"command": "printf BEGIN; head -c 1100000 /dev/zero | tr '\\000' x; printf END"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewCommandTools(completedOutputCommands{commandService})[0].Tool.(einotool.InvokableTool).InvokableRun(ctx, string(arguments))
	if err != nil {
		t.Fatal(err)
	}
	expectedOutput := "BEGIN" + strings.Repeat("x", (1<<20)-5) + "\nexit_code=0"
	if result != expectedOutput {
		t.Fatalf("prefix output mismatch: bytes=%d want=%d", len(result), len(expectedOutput))
	}
}

func (joinedCommands *joinedCommands) Cancel(ctx context.Context, taskID string) error {
	err := joinedCommands.CommandService.Cancel(ctx, taskID)
	joinedCommands.joined <- err
	return err
}

func TestExecuteStreamsAndConsumerCloseJoinsJob(t *testing.T) {
	for _, command := range []string{"printf first; sleep 30", "sleep 30"} {
		t.Run(command, func(t *testing.T) {
			filesystem := newTestLocalFilesystem(t, &filesystempkg.LocalFilesystemConfig{RootDir: t.TempDir(), VirtualMode: true})
			commandService := &joinedCommands{filesystempkg.NewCommands("thread", filesystem), make(chan error, 1)}
			defer commandService.Close(context.Background())
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			stream, err := NewCommandTools(commandService)[0].Tool.(einotool.StreamableTool).StreamableRun(ctx, `{"command":"`+command+`"}`)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(command, "printf") {
				chunk, err := stream.Recv()
				if err != nil || chunk != "first" {
					t.Fatalf("first chunk=%q err=%v", chunk, err)
				}
			}
			stream.Close()
			select {
			case err := <-commandService.joined:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("command survived consumer close")
			}
		})
	}
}

func TestExecuteStreamAndInvokeShareResultAndErrors(t *testing.T) {
	filesystem := newTestLocalFilesystem(t, &filesystempkg.LocalFilesystemConfig{RootDir: t.TempDir(), VirtualMode: true})
	commandService := filesystempkg.NewCommands("thread", filesystem)
	defer commandService.Close(context.Background())
	executeTool := NewCommandTools(commandService)[0].Tool
	for _, arguments := range []string{`{"command":"printf hello"}`, `{"command":"printf failed; exit 7"}`} {
		stream, err := executeTool.(einotool.StreamableTool).StreamableRun(context.Background(), arguments)
		if err != nil {
			t.Fatal(err)
		}
		var outputBuilder strings.Builder
		var streamErr error
		for {
			chunk, err := stream.Recv()
			if err != nil {
				if err != io.EOF {
					streamErr = err
				}
				break
			}
			outputBuilder.WriteString(chunk)
		}
		stream.Close()
		result, invokeErr := executeTool.(einotool.InvokableTool).InvokableRun(context.Background(), arguments)
		if result != outputBuilder.String() || (streamErr == nil) != (invokeErr == nil) {
			t.Fatalf("stream=%q/%v invoke=%q/%v", outputBuilder.String(), streamErr, result, invokeErr)
		}
		if strings.Contains(arguments, "exit 7") && (invokeErr == nil || !strings.Contains(invokeErr.Error(), "7")) {
			t.Fatalf("lost exit error: %v", invokeErr)
		}
	}
}

func TestExecuteTimeoutAndParentCancellation(t *testing.T) {
	filesystem := newTestLocalFilesystem(t, &filesystempkg.LocalFilesystemConfig{RootDir: t.TempDir(), VirtualMode: true})
	commandService := filesystempkg.NewCommands("thread", filesystem)
	defer commandService.Close(context.Background())
	executeTool := NewCommandTools(commandService)[0].Tool.(einotool.InvokableTool)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := executeTool.InvokableRun(ctx, `{"command":"sleep 30","timeout_ms":20}`)
	if err == nil || !strings.Contains(err.Error(), "timed out") || ctx.Err() != nil {
		t.Fatalf("command deadline must not cancel parent: %v parent=%v", err, ctx.Err())
	}
	parentCtx, cancelParent := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancelParent()
	_, err = executeTool.InvokableRun(parentCtx, `{"command":"sleep 30"}`)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lost parent cancellation: %v", err)
	}
}

func TestShellJobCanBeAwaitedAfterStartingRunContextEnds(t *testing.T) {
	filesystem, err := filesystempkg.NewLocalFilesystem(&filesystempkg.LocalFilesystemConfig{RootDir: t.TempDir(), VirtualMode: true}, "thread")
	if err != nil {
		t.Fatal(err)
	}
	defer filesystem.Close(context.Background())
	commandDescriptors := NewCommandTools(filesystem)
	shellTool := commandDescriptors[1].Tool.(einotool.InvokableTool)
	awaitShellTool := commandDescriptors[2].Tool.(einotool.InvokableTool)
	ctx, cancel := context.WithCancel(context.Background())
	output, err := shellTool.InvokableRun(ctx, `{"command":"sleep 0.1; printf finished","timeout_ms":10}`)
	if err != nil {
		t.Fatal(err)
	}
	taskID := strings.TrimPrefix(strings.Fields(output)[0], "task_id=")
	cancel()
	result, err := awaitShellTool.InvokableRun(context.Background(), fmt.Sprintf(`{"task_id":%q,"timeout_ms":1000}`, taskID))
	if err != nil || !strings.Contains(result, "status=done") || !strings.Contains(result, "finished") {
		t.Fatalf("await=%q err=%v", result, err)
	}
}
