package distributed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"eino-cli/deepagent/core/modelhub"
	manager "eino-cli/deepagent/manager/compat"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFactoryMCPSessionsCloseOnEmptyDiscoveryFailureAndCleanup(t *testing.T) {
	for _, mode := range []string{"empty", "failure", "pages"} {
		t.Run(mode, func(t *testing.T) {
			pages, closed := 0, 0
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				w := httptest.NewRecorder()
				if r.Method == http.MethodDelete {
					closed++
					if r.Header.Get("Mcp-Session-Id") != "session" {
						t.Error("missing close session")
					}
					w.WriteHeader(http.StatusNoContent)
					return w.Result(), nil
				}
				var req struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
					Params map[string]any  `json:"params"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					return nil, err
				}
				var result any
				switch req.Method {
				case "initialize":
					w.Header().Set("Mcp-Session-Id", "session")
					result = map[string]any{"protocolVersion": "2025-03-26"}
				case "notifications/initialized":
					w.WriteHeader(http.StatusAccepted)
					return w.Result(), nil
				case "tools/list":
					pages++
					if mode == "failure" {
						w.WriteHeader(http.StatusInternalServerError)
						return w.Result(), nil
					}
					result = map[string]any{"tools": []any{}}
					if mode == "pages" {
						name, cursor := "first", "next"
						if pages == 2 {
							name, cursor = "second", ""
							if req.Params["cursor"] != "next" {
								t.Error("missing next cursor")
							}
						}
						result = map[string]any{"tools": []any{map[string]any{"name": name, "inputSchema": map[string]any{"type": "object"}}}, "nextCursor": cursor}
					}
				default:
					t.Errorf("unexpected method %s", req.Method)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
				return w.Result(), nil
			})}
			cfg := Config{Manager: manager.Config{Namespace: "mcp", MySQLDSN: "unused", RedisAddr: "unused"}, DefaultModel: "test", Models: []modelhub.Config{{Name: "test", Provider: "openai", Model: "test", APIKey: "test"}}, MCP: []MCPConfig{{Name: "test", URL: "http://mcp.test", HTTPClient: client}}}
			factory, cleanup, err := NewFactory(context.Background(), manager.NewMemory("mcp"), cfg)
			if mode == "failure" {
				if err == nil || factory != nil {
					t.Fatalf("discovery failure hidden: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				defer cleanup()
				if mode == "pages" && (pages != 2 || closed != 0) {
					t.Fatalf("pages=%d premature closes=%d", pages, closed)
				}
				if mode == "empty" && closed != 1 {
					t.Fatal("empty session retained")
				}
				cleanup()
				cleanup()
			}
			if closed != 1 {
				t.Fatalf("close count=%d", closed)
			}
		})
	}
}
