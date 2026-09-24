package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"eino-cli/deepagent/core/types"
)

func TestToolObservabilityPreservesResultsAndLogsMetadataOnly(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		err     error
		isError bool
	}{
		{"success", nil, false}, {"failure", errors.New("boom"), false}, {"cancellation", context.Canceled, false}, {"tool result failure", nil, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var buf bytes.Buffer
			mw := NewToolCallObservability()
			mw.Logger = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			want := &types.ToolResult{CallID: "call", Content: "private-result", IsError: scenario.isError}
			calls := 0
			wrapped := mw.WrapTool(func(_ context.Context, call types.ToolCall) (*types.ToolResult, error) {
				calls++
				if call.Arguments != "private-input" {
					t.Fatal("arguments changed")
				}
				return want, scenario.err
			})
			got, err := wrapped(context.Background(), types.ToolCall{ID: "call", Name: "execute", Arguments: "private-input"})
			if got != want || err != scenario.err || calls != 1 {
				t.Fatalf("passthrough changed: result=%v err=%v calls=%d", got, err, calls)
			}
			var record map[string]any
			if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
				t.Fatal(err)
			}
			kind := "tool.exit"
			if scenario.err != nil || scenario.isError {
				kind = "tool.error"
			}
			if record["msg"] != kind || record["name"] != "execute" || record["in_size"] != float64(len("private-input")) || record["dur"] == nil {
				t.Fatalf("metadata missing: %v", record)
			}
			if kind == "tool.exit" && record["out_size"] != float64(len(want.Content)) {
				t.Fatalf("output size missing: %v", record)
			}
			if scenario.err != nil && record["err"] != scenario.err.Error() {
				t.Fatalf("error missing: %v", record)
			}
			if strings.Contains(buf.String(), "private-input") || strings.Contains(buf.String(), "private-result") {
				t.Fatalf("content leaked: %s", buf.String())
			}
		})
	}
}
