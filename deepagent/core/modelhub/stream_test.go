package modelhub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestCompatibleProvidersStreamToolCalls(t *testing.T) {
	for _, provider := range []string{"openai", "openai-compatible", "kimi", "moonshot", "ark", " ARK ", " KIMI "} {
		t.Run(provider, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v3/chat/completions" || r.Header.Get("Authorization") != "Bearer test-key" {
					t.Errorf("unexpected route/auth: %s", r.URL.Path)
				}
				var body struct {
					Model     string            `json:"model"`
					Stream    bool              `json:"stream"`
					MaxTokens int               `json:"max_tokens"`
					Tools     []json.RawMessage `json:"tools"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body.Model != "test-model" || !body.Stream || body.MaxTokens != 123 || len(body.Tools) != 1 {
					t.Errorf("request=%+v", body)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"id\":\"response\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call\",\"type\":\"function\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{\\\"path\\\":\"}}]},\"finish_reason\":null}]}\n\n")
				fmt.Fprint(w, "data: {\"id\":\"response\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"a\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			m, err := New(context.Background(), Config{Name: "test", Provider: provider, Model: "test-model", BaseURL: server.URL + "/api/v3", APIKey: "test-key", MaxTokens: 123})
			if err != nil {
				t.Fatal(err)
			}
			m, err = m.WithTools([]*schema.ToolInfo{{Name: "read_file", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"path": {Type: schema.String, Required: true}})}})
			if err != nil {
				t.Fatal(err)
			}
			stream, err := m.Stream(context.Background(), []*schema.Message{schema.UserMessage("read a")})
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			var chunks []*schema.Message
			for {
				chunk, err := stream.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				chunks = append(chunks, chunk)
			}
			out, err := schema.ConcatMessages(chunks)
			if err != nil {
				t.Fatal(err)
			}
			if len(out.ToolCalls) != 1 || out.ToolCalls[0].ID != "call" || out.ToolCalls[0].Function.Name != "read_file" || out.ToolCalls[0].Function.Arguments != `{"path":"a"}` {
				t.Fatalf("tool calls=%+v", out.ToolCalls)
			}
		})
	}
}

func TestArkProviderErrorsPropagate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"invalid credentials","type":"authentication_error","code":"invalid_api_key"}}`)
	}))
	defer server.Close()
	m, err := New(context.Background(), Config{Name: "test", Provider: "ark", Model: "test", BaseURL: server.URL, APIKey: "test"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := m.Stream(context.Background(), []*schema.Message{schema.UserMessage("hello")})
	if stream != nil {
		stream.Close()
	}
	if err == nil {
		t.Fatal("provider error replaced with a successful response")
	}
}

func TestClaudeStreamsToolCallsAndThinkingConfiguration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("X-Api-Key") != "test-key" {
			t.Errorf("unexpected route/auth: %s", r.URL.Path)
		}
		var body struct {
			Stream    bool              `json:"stream"`
			MaxTokens int               `json:"max_tokens"`
			Tools     []json.RawMessage `json:"tools"`
			Thinking  struct {
				Type   string `json:"type"`
				Budget int    `json:"budget_tokens"`
			} `json:"thinking"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if !body.Stream || len(body.Tools) != 1 || body.Thinking.Type != "enabled" || body.Thinking.Budget != 1024 || body.MaxTokens <= 1024 {
			t.Errorf("request=%+v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []struct{ name, data string }{
			{"message_start", `{"type":"message_start","message":{"id":"msg","type":"message","role":"assistant","model":"test-model","content":[],"usage":{"input_tokens":5,"output_tokens":0}}}`},
			{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call","name":"read_file","input":{}}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"a\"}"}}`},
			{"content_block_stop", `{"type":"content_block_stop","index":0}`},
			{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}`},
			{"message_stop", `{"type":"message_stop"}`},
		} {
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.name, event.data)
		}
	}))
	defer server.Close()
	m, err := New(context.Background(), Config{Name: "test", Provider: "claude", Model: "test-model", BaseURL: server.URL, APIKey: "test-key", SupportsThinking: true, ThinkingBudgetTokens: 1024, MaxTokens: 512})
	if err != nil {
		t.Fatal(err)
	}
	m, err = m.WithTools([]*schema.ToolInfo{{Name: "read_file", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"path": {Type: schema.String, Required: true}})}})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := m.Stream(context.Background(), []*schema.Message{schema.UserMessage("read a")})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var chunks []*schema.Message
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		chunks = append(chunks, chunk)
	}
	out, err := schema.ConcatMessages(chunks)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.ToolCalls) != 1 || out.ToolCalls[0].ID != "call" || out.ToolCalls[0].Function.Arguments != `{"path":"a"}` {
		t.Fatalf("calls=%+v", out.ToolCalls)
	}
}

func TestModelConfigurationNormalizationReachesProvider(t *testing.T) {
	for _, effort := range []string{"", "   ", "LOW", "  Medium ", "high"} {
		t.Run(effort, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model  string `json:"model"`
					Effort string `json:"reasoning_effort"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body.Model != "configured-model" || body.Effort != strings.ToLower(strings.TrimSpace(effort)) || r.Header.Get("Authorization") != "Bearer test-key" {
					t.Errorf("request=%+v authorization normalized=%t", body, r.Header.Get("Authorization") == "Bearer test-key")
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
			}))
			defer server.Close()
			m, err := New(context.Background(), Config{Name: " name ", Provider: " openai ", Model: " configured-model ", BaseURL: " " + server.URL + "/v1 ", APIKey: " test-key ", ReasoningEffort: effort})
			if err != nil {
				t.Fatal(err)
			}
			out, err := m.Generate(context.Background(), []*schema.Message{schema.UserMessage("hi")})
			if err != nil || out == nil || out.Content != "ok" {
				t.Fatalf("out=%v err=%v", out, err)
			}
		})
	}
}
