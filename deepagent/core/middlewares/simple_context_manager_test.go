package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

func TestLegacyContextHooksShareConversationAndSnapshot(t *testing.T) {
	ctx := context.Background()
	history := NewSimpleContextManager()
	request, err := history.ModifyModelRequest(ctx, []*schema.Message{schema.SystemMessage("prompt")}, []*schema.Message{schema.UserMessage("question")}, nil)
	if err != nil || len(request) != 2 {
		t.Fatalf("request=%v %v", request, err)
	}
	stream, err := history.ModifyModelStreamResponse(ctx, schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("an", nil), schema.AssistantMessage("swer", nil)}), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	output := ""
	for {
		message, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		output += message.Content
	}
	messages := history.History(ctx)
	if output != "answer" || len(messages) != 2 || messages[0].Content != "question" || messages[1].Content != "answer" {
		t.Fatalf("output=%q history=%v", output, messages)
	}
	raw := history.MarshalRuntimeState()
	var snapshot []*schema.Message
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil || len(snapshot) != 2 {
		t.Fatalf("snapshot=%s %v", raw, err)
	}
	restored := NewSimpleContextManager()
	if err := restored.UnmarshalRuntimeState(raw); err != nil {
		t.Fatal(err)
	}
	if _, err := restored.ModifyModelRequest(ctx, nil, []*schema.Message{schema.UserMessage("next")}, nil); err != nil {
		t.Fatal(err)
	}
	if len(restored.History(ctx)) != 3 || len(history.History(ctx)) != 2 {
		t.Fatal("restored conversation shared or lost history")
	}
	before := restored.MarshalRuntimeState()
	if err := restored.UnmarshalRuntimeState("broken"); err == nil {
		t.Fatal("invalid state accepted")
	}
	if restored.MarshalRuntimeState() != before {
		t.Fatal("failed restore mutated history")
	}
}

func TestLegacyContextStreamCloseStopsSilentProvider(t *testing.T) {
	for _, firstChunk := range []bool{false, true} {
		t.Run(fmt.Sprint(firstChunk), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			upstream, writer := schema.Pipe[*schema.Message](0)
			upstream.SetAutomaticClose()
			defer upstream.Close()
			stopped := make(chan struct{})
			go func() {
				defer close(stopped)
				defer writer.Close()
				if firstChunk && writer.Send(schema.AssistantMessage("partial", nil), nil) {
					return
				}
				ticker := time.NewTicker(10 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						if writer.Send(nil, nil) {
							return
						}
					}
				}
			}()
			history := NewSimpleContextManager()
			out, err := history.ModifyModelStreamResponse(ctx, upstream, nil)
			if err != nil {
				t.Fatal(err)
			}
			if firstChunk {
				msg, err := out.Recv()
				if err != nil || msg.Content != "partial" {
					t.Fatalf("chunk=%v %v", msg, err)
				}
			}
			out.Close()
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("consumer close left upstream running")
			}
			if len(history.History(ctx)) != 0 {
				t.Fatal("abandoned response entered history")
			}
		})
	}
}
