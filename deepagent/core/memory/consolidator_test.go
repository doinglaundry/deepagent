package memory

import (
	"context"
	"encoding/json"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"testing"
)

type graphMemoryModel struct {
	n     int
	bound []*schema.ToolInfo
}

func (m *graphMemoryModel) WithTools(ts []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	m.bound = ts
	return m, nil
}
func (m *graphMemoryModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	panic("consolidator must use graph streaming")
}
func (m *graphMemoryModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.n++
	if m.n == 1 {
		arg, _ := json.Marshal(map[string]string{"file_path": "MEMORY.md", "content": "# Verified memory\nUser uses Go."})
		return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "write", Function: schema.FunctionCall{Name: "write_file", Arguments: string(arg)}}}}}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("done", nil)}), nil
}
func TestConsolidatorRunsScopedGraphAndValidatesWrittenArtifact(t *testing.T) {
	m := &graphMemoryModel{}
	out, e := AgentConsolidator(m, t.TempDir())(context.Background(), "old", "sources")
	if e != nil || out != "# Verified memory\nUser uses Go." {
		t.Fatal(out, e)
	}
	for _, info := range m.bound {
		if info.Name != "read_file" && info.Name != "ls" && info.Name != "write_file" {
			t.Fatal("unsafe memory capability", info.Name)
		}
	}
	if m.n != 2 {
		t.Fatal("did not execute tool and continue model")
	}
}
