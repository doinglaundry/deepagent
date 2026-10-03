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
	var requestedMethods []string
	handler := http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		var rpcRequest struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		err := json.NewDecoder(request.Body).Decode(&rpcRequest)
		if err != nil {
			t.Error(err)
			return
		}
		requestedMethods = append(requestedMethods, rpcRequest.Method)
		var rpcResult any
		switch rpcRequest.Method {
		case "initialize":
			responseWriter.Header().Set("Mcp-Session-Id", "session")
			rpcResult = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}}
		case "notifications/initialized":
			responseWriter.WriteHeader(202)
			return
		case "tools/list":
			if request.Header.Get("Mcp-Session-Id") != "session" {
				t.Error("missing session header")
			}
			rpcResult = map[string]any{"tools": []any{map[string]any{"name": "lookup", "description": "Lookup info", "inputSchema": map[string]any{"type": "object"}, "annotations": map[string]bool{"readOnlyHint": true}}}}
		case "tools/call":
			rpcResult = map[string]any{"content": []any{map[string]string{"type": "text", "text": "answer"}}}
		}
		responseWriter.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(responseWriter).Encode(map[string]any{"jsonrpc": "2.0", "id": rpcRequest.ID, "result": rpcResult})
	})
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		responseRecorder := httptest.NewRecorder()
		handler.ServeHTTP(responseRecorder, request)
		return responseRecorder.Result(), nil
	})}
	toolDescriptors, err := LoadMCP(context.Background(), []MCPConfig{{Name: "test", URL: "http://mcp.test", HTTPClient: httpClient}})
	if err != nil {
		t.Fatal(err)
	}
	if len(toolDescriptors) != 1 {
		t.Fatalf("tools %v", toolDescriptors)
	}
	if !toolDescriptors[0].ReadOnly || toolDescriptors[0].RequiresApproval || toolDescriptors[0].ParallelSafe || toolDescriptors[0].ReturnDirect {
		t.Fatalf("read-only MCP capabilities = %+v", toolDescriptors[0])
	}
	toolInfo, _ := toolDescriptors[0].Tool.Info(context.Background())
	if toolInfo.Name != "mcp_test_lookup" {
		t.Fatalf("name %s", toolInfo.Name)
	}
	toolResult, err := toolDescriptors[0].Tool.(tool.InvokableTool).InvokableRun(context.Background(), "{}")
	if err != nil || toolResult == "" {
		t.Fatalf("out %s err %v", toolResult, err)
	}
	if len(requestedMethods) != 4 {
		t.Fatalf("methods %v", requestedMethods)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestMCP_DiscoveryPaginationAndClose(t *testing.T) {
	for _, discoveryMode := range []string{"pages", "empty", "failure"} {
		t.Run(discoveryMode, func(t *testing.T) {
			pageCount, closeCount := 0, 0
			httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				responseRecorder := httptest.NewRecorder()
				if request.Method == http.MethodDelete {
					closeCount++
					responseRecorder.WriteHeader(http.StatusNoContent)
					return responseRecorder.Result(), nil
				}
				var rpcRequest struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
					Params map[string]any  `json:"params"`
				}
				err := json.NewDecoder(request.Body).Decode(&rpcRequest)
				if err != nil {
					return nil, err
				}
				var rpcResult any
				switch rpcRequest.Method {
				case "initialize":
					responseRecorder.Header().Set("Mcp-Session-Id", "session")
					rpcResult = map[string]any{"protocolVersion": "2025-03-26"}
				case "notifications/initialized":
					responseRecorder.WriteHeader(202)
					return responseRecorder.Result(), nil
				case "tools/list":
					pageCount++
					if discoveryMode == "failure" {
						responseRecorder.WriteHeader(500)
						return responseRecorder.Result(), nil
					}
					if discoveryMode == "empty" {
						rpcResult = map[string]any{"tools": []any{}}
						break
					}
					toolName := "first"
					nextCursor := "next"
					if pageCount == 2 {
						if rpcRequest.Params["cursor"] != "next" {
							t.Errorf("missing cursor: %v", rpcRequest.Params)
						}
						toolName = "second"
						nextCursor = ""
					}
					rpcResult = map[string]any{"tools": []any{map[string]any{"name": toolName, "inputSchema": map[string]any{"type": "object"}}}, "nextCursor": nextCursor}
				}
				responseRecorder.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(responseRecorder).Encode(map[string]any{"jsonrpc": "2.0", "id": rpcRequest.ID, "result": rpcResult})
				return responseRecorder.Result(), nil
			})}
			toolDescriptors, err := LoadMCP(context.Background(), []MCPConfig{{Name: "test", URL: "http://mcp.test", HTTPClient: httpClient}})
			if discoveryMode == "failure" {
				if err == nil {
					t.Fatal("discovery failure hidden")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if discoveryMode == "pages" && (len(toolDescriptors) != 2 || pageCount != 2) {
				t.Fatalf("tools=%d pages=%d", len(toolDescriptors), pageCount)
			}
			for _, descriptor := range toolDescriptors {
				if descriptor.ReadOnly || !descriptor.RequiresApproval || descriptor.ParallelSafe || descriptor.ReturnDirect {
					t.Fatalf("unannotated MCP capabilities = %+v", descriptor)
				}
			}
			CloseMCP(toolDescriptors)
			CloseMCP(toolDescriptors)
			if closeCount != 1 {
				t.Fatalf("session close count=%d; expected exactly one", closeCount)
			}
		})
	}
}
