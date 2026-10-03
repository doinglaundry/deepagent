package memory

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type graphMemoryModel struct {
	n     int
	bound []*schema.ToolInfo
}

func (chatModel *graphMemoryModel) WithTools(toolInfos []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	chatModel.bound = toolInfos
	return chatModel, nil
}
func (chatModel *graphMemoryModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	panic("consolidator must use graph streaming")
}
func (chatModel *graphMemoryModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	chatModel.n++
	if chatModel.n == 1 {
		arguments, _ := json.Marshal(map[string]string{"path": "MEMORY.md", "content": "# Verified memory\nUser uses Go."})
		return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "write", Function: schema.FunctionCall{Name: "write_file", Arguments: string(arguments)}}}}}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("done", nil)}), nil
}
func TestConsolidatorRunsScopedGraphAndValidatesWrittenArtifact(t *testing.T) {
	chatModel := &graphMemoryModel{}
	summary, err := NewAgentConsolidator(chatModel, t.TempDir())(context.Background(), "old", "sources")
	if err != nil || summary != "# Verified memory\nUser uses Go." {
		t.Fatal(summary, err)
	}
	for _, info := range chatModel.bound {
		if info.Name != "read_file" && info.Name != "list_files" && info.Name != "write_file" {
			t.Fatal("unsafe memory capability", info.Name)
		}
	}
	if chatModel.n != 2 {
		t.Fatal("did not execute tool and continue model")
	}
}

func TestConsolidationCancelledGenerationCannotCommitBaseline(t *testing.T) {
	for _, shared := range []bool{false, true} {
		name := "local"
		if shared {
			name = "shared"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			config := Config{Root: t.TempDir(), Model: &memoryModel{}, Consolidator: func(context.Context, string, string) (string, error) { calls++; cancel(); return "stale summary", nil }}
			if shared {
				config.Store = newMemoryStore()
			}
			service, err := New(config)
			if err != nil {
				t.Fatal(err)
			}
			err = service.Observe(ctx, "scope", "source", []*schema.Message{schema.UserMessage("fact")})
			if err != nil {
				t.Fatal(err)
			}
			err = service.Consolidate(ctx, "scope")
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled consolidation published: %v", err)
			}
			snapshot, err := service.Read(context.Background(), "scope")
			if err != nil || snapshot.Summary != "" {
				t.Fatalf("cancelled summary committed: %+v %v", snapshot, err)
			}
			service.(*memoryService).c.Consolidator = func(context.Context, string, string) (string, error) { calls++; return "valid summary", nil }
			err = service.Consolidate(context.Background(), "scope")
			if err != nil || calls != 2 {
				t.Fatalf("baseline skipped failed generation: calls=%d err=%v", calls, err)
			}
		})
	}
}
