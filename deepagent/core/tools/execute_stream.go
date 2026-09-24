package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"eino-cli/deepagent/core/backend"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// executeTool streams the same thread-owned job used by shell and await_shell.
type executeTool struct{ commandTool }

func (t *executeTool) InvokableRun(ctx context.Context, raw string, opts ...einotool.Option) (string, error) {
	stream, err := t.StreamableRun(ctx, raw, opts...)
	if err != nil {
		return "", err
	}
	defer stream.Close()
	var output strings.Builder
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return output.String(), ctx.Err()
		}
		if err != nil {
			return output.String(), err
		}
		output.WriteString(chunk)
	}
}

func (t *executeTool) StreamableRun(ctx context.Context, raw string, _ ...einotool.Option) (*schema.StreamReader[string], error) {
	if t.service == nil {
		return nil, fmt.Errorf("command service is required")
	}
	var input struct {
		Command        string `json:"command"`
		WorkDir        string `json:"working_directory"`
		TimeoutMS      int    `json:"timeout_ms"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		return nil, err
	}
	timeout := t.defaultTimeout
	if input.TimeoutMS > 0 {
		timeout = time.Duration(min(input.TimeoutMS, 300_000)) * time.Millisecond
	} else if input.TimeoutSeconds > 0 {
		timeout = time.Duration(min(input.TimeoutSeconds, 300)) * time.Second
	}
	if timeout > 5*time.Minute {
		timeout = 5 * time.Minute
	}
	jobCtx, cancel := context.WithCancel(ctx)
	id, err := t.service.Start(jobCtx, backend.CommandRequest{Command: input.Command, WorkDir: input.WorkDir, Timeout: timeout, MaxOutputBytes: 1 << 20, KeepOutputPrefix: true})
	if err != nil {
		cancel()
		return nil, err
	}
	reader, writer := schema.Pipe[string](0)
	reader.SetAutomaticClose()
	stopClose := context.AfterFunc(ctx, reader.Close)
	go func() {
		defer writer.Close()
		defer stopClose()
		defer func() {
			cancel()
			// Join the process even when the stream consumer closes while silent.
			_ = t.service.Cancel(context.Background(), id)
		}()
		offset, remaining := 0, 1<<20
		for {
			waitCtx, stopWait := context.WithTimeout(jobCtx, 50*time.Millisecond)
			snapshot, err := t.service.Wait(waitCtx, id, "(?s).", offset)
			stopWait()
			if ctx.Err() != nil {
				return
			}
			if err != nil && !errors.Is(err, context.DeadlineExceeded) {
				writer.Send("", err)
				return
			}
			if snapshot == nil {
				writer.Send("", fmt.Errorf("command returned no snapshot"))
				return
			}
			chunk := snapshot.Output
			if len(chunk) > remaining {
				chunk = chunk[:remaining]
			}
			remaining -= len(chunk)
			offset = snapshot.Offset
			// Empty sends detect consumer closure without exposing heartbeat chunks.
			if writer.Send(chunk, nil) {
				return
			}
			if snapshot.Done {
				if snapshot.TimedOut {
					writer.Send("", fmt.Errorf("command timed out after %s", timeout))
				} else if snapshot.ExitCode != 0 {
					writer.Send("", fmt.Errorf("command exited with code %d", snapshot.ExitCode))
				} else {
					writer.Send("\nexit_code=0", nil)
				}
				return
			}
		}
	}()
	return schema.StreamReaderWithConvert(reader, func(chunk string) (string, error) {
		if chunk == "" {
			return "", schema.ErrNoValue
		}
		return chunk, nil
	}), nil
}
