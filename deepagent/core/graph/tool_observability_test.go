package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/tools"
	"github.com/cloudwego/eino/schema"
)

func TestRun_ToolObservabilityLogsWholeStreamOnce(t *testing.T) {
	ctx := context.Background()
	var logs bytes.Buffer
	mw := middleware.NewToolCallObservability()
	mw.Logger = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "stream_tool", Arguments: "private-arguments"}}})}}}
	a, err := New(ctx, WithConfig(&Config{Model: m, Middlewares: []middleware.Middleware{mw}, ToolDescriptors: []tools.Descriptor{{Tool: &eventStreamTool{}, ReturnDirect: true}}}))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(ctx)
	out, err := a.Run(ctx, []*schema.Message{schema.UserMessage("go")})
	if err != nil || out.Content != "onetwo" || m.calls != 1 {
		t.Fatalf("output=%v err=%v calls=%d", out, err, m.calls)
	}
	var record map[string]any
	if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
		t.Fatalf("expected one log record: %v / %s", err, logs.String())
	}
	if record["msg"] != "tool.exit" || record["out_size"] != float64(6) {
		t.Fatalf("incomplete stream logged: %v", record)
	}
	if strings.Contains(logs.String(), "private-arguments") || strings.Contains(logs.String(), "onetwo") {
		t.Fatalf("content leaked: %s", logs.String())
	}
}
