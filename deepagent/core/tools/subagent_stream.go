package tools

import (
	"context"
	"eino-cli/deepagent/core/types"
	"fmt"
	"time"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type streamingTaskTool struct{ *taskTool }

func NewStreamingTaskTool(runner ChildRunner, names ...string) einotool.BaseTool {
	return &streamingTaskTool{&taskTool{runner: runner, names: append([]string(nil), names...)}}
}
func (t *streamingTaskTool) StreamableRun(ctx context.Context, raw string, _ ...einotool.Option) (*schema.StreamReader[string], error) {
	request, err := parseChildRequest(raw)
	if err != nil {
		return nil, err
	}
	if t.runner == nil {
		return nil, fmt.Errorf("child runner is required")
	}
	ctx, cancel := context.WithCancel(ctx)
	reader, writer := schema.Pipe[string](0)
	reader.SetAutomaticClose()
	stopClose := context.AfterFunc(ctx, reader.Close)
	chunks, done := make(chan string), make(chan error, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				done <- &types.InternalError{Err: fmt.Errorf("child runner panicked: %v", recovered)}
			}
		}()
		emitted := false
		message, err := t.runner.Run(ctx, request, func(ctx context.Context, chunk *schema.Message) error {
			if chunk == nil || chunk.Content == "" {
				return nil
			}
			select {
			case chunks <- chunk.Content:
				emitted = true
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		if err == nil && message == nil {
			err = fmt.Errorf("child returned no message")
		}
		if err == nil && !emitted && message.Content != "" {
			select {
			case chunks <- message.Content:
			case <-ctx.Done():
				err = ctx.Err()
			}
		}
		done <- err
	}()
	go func() {
		defer writer.Close()
		defer cancel()
		defer stopClose()
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case chunk := <-chunks:
				if writer.Send(chunk, nil) {
					cancel()
					<-done
					return
				}
			case err := <-done:
				if err != nil {
					writer.Send("", err)
				}
				return
			case <-ticker.C:
				if writer.Send("", nil) {
					cancel()
					<-done
					return
				}
			case <-ctx.Done():
				<-done
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
