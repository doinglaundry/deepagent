package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cloudwego/eino/components/tool"
)

func TestMCPInitializesListsAndInvokesRemoteTool(t *testing.T) {
	var methods []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		err := json.NewDecoder(r.Body).Decode(&req)
		if err != nil {
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

func TestMCP_DiscoveryPaginationAndClose(t *testing.T) {
	for _, mode := range []string{"pages", "empty", "failure"} {
		t.Run(mode, func(t *testing.T) {
			pages, closed := 0, 0
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				if r.Method == http.MethodDelete {
					closed++
					rec.WriteHeader(http.StatusNoContent)
					return rec.Result(), nil
				}
				var req struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
					Params map[string]any  `json:"params"`
				}
				err := json.NewDecoder(r.Body).Decode(&req)
				if err != nil {
					return nil, err
				}
				var result any
				switch req.Method {
				case "initialize":
					rec.Header().Set("Mcp-Session-Id", "session")
					result = map[string]any{"protocolVersion": "2025-03-26"}
				case "notifications/initialized":
					rec.WriteHeader(202)
					return rec.Result(), nil
				case "tools/list":
					pages++
					if mode == "failure" {
						rec.WriteHeader(500)
						return rec.Result(), nil
					}
					if mode == "empty" {
						result = map[string]any{"tools": []any{}}
						break
					}
					name := "first"
					cursor := "next"
					if pages == 2 {
						if req.Params["cursor"] != "next" {
							t.Errorf("missing cursor: %v", req.Params)
						}
						name = "second"
						cursor = ""
					}
					result = map[string]any{"tools": []any{map[string]any{"name": name, "inputSchema": map[string]any{"type": "object"}}}, "nextCursor": cursor}
				}
				rec.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(rec).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
				return rec.Result(), nil
			})}
			loaded, err := LoadMCP(context.Background(), []MCPConfig{{Name: "test", URL: "http://mcp.test", HTTPClient: client}})
			if mode == "failure" {
				if err == nil {
					t.Fatal("discovery failure hidden")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if mode == "pages" && (len(loaded) != 2 || pages != 2) {
				t.Fatalf("tools=%d pages=%d", len(loaded), pages)
			}
			CloseMCP(loaded)
			CloseMCP(loaded)
			if closed != 1 {
				t.Fatalf("session close count=%d; expected exactly one", closed)
			}
		})
	}
}
