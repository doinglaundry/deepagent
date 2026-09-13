package engine

import (
	"context"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"io"
	"testing"
)

type childModel struct{ names []string }

func (m *childModel) WithTools(ts []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	for _, t := range ts {
		m.names = append(m.names, t.Name)
	}
	return m, nil
}
func (m *childModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	panic("must stream")
}
func (m *childModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("researched", nil)}), nil
}

type unsafeTool struct{}

func (unsafeTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "mutate"}, nil
}
func TestInternalSubagentFiltersUnknownCapabilitiesAndStreams(t *testing.T) {
	m := &childModel{}
	t1 := NewSubagentTool(Config{Model: m, Tools: []tool.BaseTool{unsafeTool{}}})
	r, e := t1.(tool.StreamableTool).StreamableRun(context.Background(), `{"task":"research"}`)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	out := ""
	for {
		chunk, e := r.Recv()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		out += chunk
	}
	if out != "researched" || len(m.names) != 0 {
		t.Fatal(out, m.names)
	}
}
