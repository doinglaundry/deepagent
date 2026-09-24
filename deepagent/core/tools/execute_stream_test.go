package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"eino-cli/deepagent/core/backend"
	einotool "github.com/cloudwego/eino/components/tool"
)

type joinedCommands struct {
	backend.CommandService
	joined chan error
}

// completedOutputCommands makes the first observation happen after process exit.
type completedOutputCommands struct{ backend.CommandService }

func (s completedOutputCommands) Wait(ctx context.Context, id, _ string, offset int) (*backend.CommandSnapshot, error) {
	return s.CommandService.Wait(ctx, id, "", offset)
}

func TestExecuteSlowConsumerRetainsFirstMiB(t *testing.T) {
	workspace := backend.NewFilesystemBackend(&backend.FilesystemBackendConfig{RootDir: t.TempDir(), VirtualMode: true})
	service := backend.NewCommands("thread", workspace)
	defer service.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := json.Marshal(map[string]string{"command": "printf BEGIN; head -c 1100000 /dev/zero | tr '\\000' x; printf END"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewCommandTools(completedOutputCommands{service})[0].(einotool.InvokableTool).InvokableRun(ctx, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	want := "BEGIN" + strings.Repeat("x", (1<<20)-5) + "\nexit_code=0"
	if result != want {
		t.Fatalf("prefix output mismatch: bytes=%d want=%d", len(result), len(want))
	}
}

func (s *joinedCommands) Cancel(ctx context.Context, id string) error {
	err := s.CommandService.Cancel(ctx, id)
	s.joined <- err
	return err
}

func TestExecuteStreamsAndConsumerCloseJoinsJob(t *testing.T) {
	for _, command := range []string{"printf first; sleep 30", "sleep 30"} {
		t.Run(command, func(t *testing.T) {
			workspace := backend.NewFilesystemBackend(&backend.FilesystemBackendConfig{RootDir: t.TempDir(), VirtualMode: true})
			service := &joinedCommands{backend.NewCommands("thread", workspace), make(chan error, 1)}
			defer service.Close(context.Background())
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			stream, err := NewCommandTools(service)[0].(einotool.StreamableTool).StreamableRun(ctx, `{"command":"`+command+`"}`)
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
			case err := <-service.joined:
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
	workspace := backend.NewFilesystemBackend(&backend.FilesystemBackendConfig{RootDir: t.TempDir(), VirtualMode: true})
	service := backend.NewCommands("thread", workspace)
	defer service.Close(context.Background())
	command := NewCommandTools(service)[0]
	for _, raw := range []string{`{"command":"printf hello"}`, `{"command":"printf failed; exit 7"}`} {
		stream, err := command.(einotool.StreamableTool).StreamableRun(context.Background(), raw)
		if err != nil {
			t.Fatal(err)
		}
		var output strings.Builder
		var streamErr error
		for {
			chunk, err := stream.Recv()
			if err != nil {
				if err != io.EOF {
					streamErr = err
				}
				break
			}
			output.WriteString(chunk)
		}
		stream.Close()
		result, invokeErr := command.(einotool.InvokableTool).InvokableRun(context.Background(), raw)
		if result != output.String() || (streamErr == nil) != (invokeErr == nil) {
			t.Fatalf("stream=%q/%v invoke=%q/%v", output.String(), streamErr, result, invokeErr)
		}
		if strings.Contains(raw, "exit 7") && (invokeErr == nil || !strings.Contains(invokeErr.Error(), "7")) {
			t.Fatalf("lost exit error: %v", invokeErr)
		}
	}
}

func TestExecuteTimeoutAndParentCancellation(t *testing.T) {
	workspace := backend.NewFilesystemBackend(&backend.FilesystemBackendConfig{RootDir: t.TempDir(), VirtualMode: true})
	service := backend.NewCommands("thread", workspace)
	defer service.Close(context.Background())
	command := NewCommandTools(service)[0].(einotool.InvokableTool)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := command.InvokableRun(ctx, `{"command":"sleep 30","timeout_ms":20}`)
	if err == nil || !strings.Contains(err.Error(), "timed out") || ctx.Err() != nil {
		t.Fatalf("command deadline must not cancel parent: %v parent=%v", err, ctx.Err())
	}
	parent, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	defer stop()
	_, err = command.InvokableRun(parent, `{"command":"sleep 30"}`)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lost parent cancellation: %v", err)
	}
}
