package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	filesystempkg "eino-cli/deepagent/graph/filesystem"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// executeTool streams the same thread-owned job used by shell and await_shell.
type executeTool struct{ commandTool }

func (executeTool *executeTool) InvokableRun(ctx context.Context, arguments string, options ...tool.Option) (string, error) {
	outputStream, err := executeTool.StreamableRun(ctx, arguments, options...)
	if err != nil {
		return "", err
	}
	defer outputStream.Close()
	var outputBuilder strings.Builder
	for {
		chunk, err := outputStream.Recv()
		if errors.Is(err, io.EOF) {
			return outputBuilder.String(), ctx.Err()
		}
		if err != nil {
			return outputBuilder.String(), err
		}
		outputBuilder.WriteString(chunk)
	}
}

func (executeTool *executeTool) StreamableRun(ctx context.Context, arguments string, _ ...tool.Option) (*schema.StreamReader[string], error) {
	if executeTool.commandService == nil {
		return nil, fmt.Errorf("command service is required")
	}
	var commandArgs struct {
		Command        string `json:"command"`
		WorkDir        string `json:"working_directory"`
		TimeoutMS      int    `json:"timeout_ms"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	decodeErr := json.Unmarshal([]byte(arguments), &commandArgs)
	if decodeErr != nil {
		return nil, decodeErr
	}
	timeout := executeTool.defaultTimeout
	if commandArgs.TimeoutMS > 0 {
		timeout = time.Duration(min(commandArgs.TimeoutMS, 300_000)) * time.Millisecond
	} else if commandArgs.TimeoutSeconds > 0 {
		timeout = time.Duration(min(commandArgs.TimeoutSeconds, 300)) * time.Second
	}
	if timeout > 5*time.Minute {
		timeout = 5 * time.Minute
	}
	commandCtx, cancelCommand := context.WithCancel(ctx)
	taskID, err := executeTool.commandService.Start(commandCtx, filesystempkg.CommandRequest{Command: commandArgs.Command, WorkDir: commandArgs.WorkDir, Timeout: timeout, MaxOutputBytes: 1 << 20, KeepOutputPrefix: true})
	if err != nil {
		cancelCommand()
		return nil, err
	}
	streamReader, streamWriter := schema.Pipe[string](0)
	streamReader.SetAutomaticClose()
	stopAutomaticClose := context.AfterFunc(ctx, streamReader.Close)
	go func() {
		defer streamWriter.Close()
		defer stopAutomaticClose()
		defer func() {
			cancelCommand()
			// Join the process even when the stream consumer closes while silent.
			_ = executeTool.commandService.Cancel(context.Background(), taskID)
		}()
		outputOffset, remainingBytes := 0, 1<<20
		for {
			waitCtx, cancelWait := context.WithTimeout(commandCtx, 50*time.Millisecond)
			commandSnapshot, err := executeTool.commandService.Wait(waitCtx, taskID, "(?s).", outputOffset)
			cancelWait()
			if ctx.Err() != nil {
				return
			}
			if err != nil && !errors.Is(err, context.DeadlineExceeded) {
				streamWriter.Send("", err)
				return
			}
			if commandSnapshot == nil {
				streamWriter.Send("", fmt.Errorf("command returned no snapshot"))
				return
			}
			chunk := commandSnapshot.Output
			if len(chunk) > remainingBytes {
				chunk = chunk[:remainingBytes]
			}
			remainingBytes -= len(chunk)
			outputOffset = commandSnapshot.Offset
			// Empty sends detect consumer closure without exposing heartbeat chunks.
			if streamWriter.Send(chunk, nil) {
				return
			}
			if commandSnapshot.Done {
				if commandSnapshot.TimedOut {
					streamWriter.Send("", fmt.Errorf("command timed out after %s", timeout))
				} else if commandSnapshot.ExitCode != 0 {
					streamWriter.Send("", fmt.Errorf("command exited with code %d", commandSnapshot.ExitCode))
				} else {
					streamWriter.Send("\nexit_code=0", nil)
				}
				return
			}
		}
	}()
	return schema.StreamReaderWithConvert(streamReader, func(chunk string) (string, error) {
		if chunk == "" {
			return "", schema.ErrNoValue
		}
		return chunk, nil
	}), nil
}
