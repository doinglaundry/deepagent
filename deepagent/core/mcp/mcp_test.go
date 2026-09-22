package mcp

import (
	"context"
	"encoding/json"
	"github.com/cloudwego/eino/components/tool"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMCPInitializesListsAndInvokesRemoteTool(t *testing.T) {
	var methods []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		methods = append(methods, req.Method)
		var result any
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "session")
			result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}}
		case "notifications/initialized":
			w.WriteHeader(202)
			return
		case "tools/list":
			if r.Header.Get("Mcp-Session-Id") != "session" {
				t.Error("missing session header")
			}
			result = map[string]any{"tools": []any{map[string]any{"name": "lookup", "description": "Lookup info", "inputSchema": map[string]any{"type": "object"}, "annotations": map[string]bool{"readOnlyHint": true}}}}
		case "tools/call":
			result = map[string]any{"content": []any{map[string]string{"type": "text", "text": "answer"}}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	})
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		return rec.Result(), nil
	})}
	tools, err := LoadMCP(context.Background(), []MCPConfig{{Name: "test", URL: "http://mcp.test", HTTPClient: client}})
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 {
		t.Fatalf("tools %v", tools)
	}
	info, _ := tools[0].Info(context.Background())
	if info.Name != "mcp_test_lookup" {
		t.Fatalf("name %s", info.Name)
	}
	out, err := tools[0].(tool.InvokableTool).InvokableRun(context.Background(), "{}")
	if err != nil || out == "" {
		t.Fatalf("out %s err %v", out, err)
	}
	if len(methods) != 4 {
		t.Fatalf("methods %v", methods)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
