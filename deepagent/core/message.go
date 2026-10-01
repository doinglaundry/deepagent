package deepagents

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/cloudwego/eino/schema"
)

func CopyMessage(msg *schema.Message) *schema.Message {
	if msg == nil {
		return nil
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil
	}
	var out schema.Message
	if json.Unmarshal(b, &out) != nil {
		return nil
	}
	return &out
}

// StreamMessageMerger adapts legacy callback streams to the model collector.
// All mutable collection state belongs to one Merge invocation.
type StreamMessageMerger struct {
	onChunk func(context.Context, *schema.Message)
}

func NewStreamMessageMerger(onChunk func(context.Context, *schema.Message)) *StreamMessageMerger {
	return &StreamMessageMerger{onChunk: onChunk}
}

func (s *StreamMessageMerger) Merge(ctx context.Context, stream *schema.StreamReader[*schema.Message]) (*schema.Message, error) {
	var chunks []*schema.Message
	buffer := toolCallBuffer{}
	for {
		{
			err := ctx.Err()
			if err != nil {
				return nil, err
			}
		}
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if chunk == nil {
			continue
		}
		if s.onChunk != nil {
			s.onChunk(ctx, chunk)
		}
		{
			_, err := buffer.add(chunk.ToolCalls)
			if err != nil {
				return nil, err
			}
		}
		part := *chunk
		part.ToolCalls = nil
		chunks = append(chunks, &part)
	}
	if len(chunks) == 0 {
		return nil, nil
	}
	message, err := schema.ConcatMessages(chunks)
	if err != nil {
		return nil, err
	}
	message.ToolCalls, err = buffer.finish()
	return message, err
}
