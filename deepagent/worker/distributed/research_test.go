package distributed

import (
	"context"
	"fmt"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"io"
	"strings"
	"testing"
)

type childModel struct {
	names   []string
	address string
}

func (m *childModel) WithTools(ts []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	for _, t := range ts {
		m.names = append(m.names, t.Name)
	}
	return m, nil
}
func (m *childModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	panic("must stream")
}
func (m *childModel) Stream(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.address = compose.GetCurrentAddress(ctx).String()
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("researched", nil)}), nil
}

type unsafeTool struct{}

func (unsafeTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "mutate"}, nil
}
func TestInternalSubagentFiltersUnknownCapabilitiesAndStreams(t *testing.T) {
	m := &childModel{}
	t1, err := newResearchTool(context.Background(), m, []tool.BaseTool{unsafeTool{}})
	if err != nil {
		t.Fatal(err)
	}
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
	if !strings.Contains(m.address, "deepagent") {
		t.Fatalf("child did not use canonical graph: %q", m.address)
	}
	if out != "researched" || len(m.names) != 0 {
		t.Fatal(out, m.names)
	}
}

type researchTool struct {
	name     string
	approval bool
	calls    int
}

func (t *researchTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name}, nil
}
func (*researchTool) ReadOnly() bool           { return true }
func (t *researchTool) RequiresApproval() bool { return t.approval }
func (t *researchTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	t.calls++
	return "evidence", nil
}

type researchModel struct {
	childModel
	calls int
}

func (m *researchModel) WithTools(ts []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	_, err := m.childModel.WithTools(ts)
	return m, err
}
func (m *researchModel) Stream(ctx context.Context, messages []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.address = compose.GetCurrentAddress(ctx).String()
	m.calls++
	if m.calls == 1 {
		return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("", []schema.ToolCall{{ID: "read", Function: schema.FunctionCall{Name: "read", Arguments: "{}"}}})}), nil
	}
	found := false
	for _, msg := range messages {
		if msg.Role == schema.Tool && msg.ToolCallID == "read" && msg.Content == "evidence" {
			found = true
		}
	}
	if !found {
		return nil, fmt.Errorf("tool result missing from child conversation")
	}
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("researched", nil)}), nil
}
func TestInternalSubagentUsesCanonicalToolRoundTrip(t *testing.T) {
	m := &researchModel{}
	read := &researchTool{name: "read"}
	approval := &researchTool{name: "approval", approval: true}
	recursive := &researchTool{name: "internal_subagent"}
	ask := &researchTool{name: "ask_user"}
	t1, err := newResearchTool(context.Background(), m, []tool.BaseTool{read, approval, recursive, ask, unsafeTool{}})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := t1.(tool.StreamableTool).StreamableRun(context.Background(), `{"task":"research"}`)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var out strings.Builder
	for {
		chunk, err := reader.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		out.WriteString(chunk)
	}
	if out.String() != "researched" || m.calls != 2 || read.calls != 1 || approval.calls != 0 || recursive.calls != 0 || ask.calls != 0 {
		t.Fatalf("out=%s model=%d read=%d", out.String(), m.calls, read.calls)
	}
	if len(m.names) == 0 {
		t.Fatal("read-only capability lost")
	}
	for _, name := range m.names {
		if name != "read" {
			t.Fatalf("unexpected child capability: %s", name)
		}
	}
	if !strings.Contains(m.address, "deepagent") {
		t.Fatalf("noncanonical graph %s", m.address)
	}
}

func TestInternalSubagentPreservesSchemaAndRejectsEmptyTask(t *testing.T) {
	t1, err := newResearchTool(context.Background(), &childModel{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	info, err := t1.Info(context.Background())
	if err != nil || info.Name != "internal_subagent" {
		t.Fatalf("info=%+v err=%v", info, err)
	}
	js, err := info.ParamsOneOf.ToJSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	if len(js.Required) != 1 || js.Required[0] != "task" {
		t.Fatalf("schema=%+v", js)
	}
	for _, input := range []string{`{}`, `{"task":"  "}`, `{"task":1}`, `broken`} {
		reader, err := t1.(tool.StreamableTool).StreamableRun(context.Background(), input)
		if err == nil {
			reader.Close()
			t.Fatalf("invalid task accepted: %s", input)
		}
	}
}
