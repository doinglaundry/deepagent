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
			server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/api/v3/chat/completions" || request.Header.Get("Authorization") != "Bearer test-key" {
					t.Errorf("unexpected route/auth: %s", request.URL.Path)
				}
				var requestBody struct {
					Model     string            `json:"model"`
					Stream    bool              `json:"stream"`
					MaxTokens int               `json:"max_tokens"`
					Tools     []json.RawMessage `json:"tools"`
				}
				err := json.NewDecoder(request.Body).Decode(&requestBody)
				if err != nil {
					t.Error(err)
				}
				if requestBody.Model != "test-model" || !requestBody.Stream || requestBody.MaxTokens != 123 || len(requestBody.Tools) != 1 {
					t.Errorf("request=%+v", requestBody)
				}
				responseWriter.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(responseWriter, "data: {\"id\":\"response\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call\",\"type\":\"function\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{\\\"path\\\":\"}}]},\"finish_reason\":null}]}\n\n")
				fmt.Fprint(responseWriter, "data: {\"id\":\"response\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"a\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			chatModel, err := New(context.Background(), Config{Name: "test", Provider: provider, Model: "test-model", BaseURL: server.URL + "/api/v3", APIKey: "test-key", MaxTokens: 123})
			if err != nil {
				t.Fatal(err)
			}
			chatModel, err = chatModel.WithTools([]*schema.ToolInfo{{Name: "read_file", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"path": {Type: schema.String, Required: true}})}})
			if err != nil {
				t.Fatal(err)
			}
			messageStream, err := chatModel.Stream(context.Background(), []*schema.Message{schema.UserMessage("read a")})
			if err != nil {
				t.Fatal(err)
			}
			defer messageStream.Close()
			var messageChunks []*schema.Message
			for {
				messageChunk, err := messageStream.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				messageChunks = append(messageChunks, messageChunk)
			}
			message, err := schema.ConcatMessages(messageChunks)
			if err != nil {
				t.Fatal(err)
			}
			if len(message.ToolCalls) != 1 || message.ToolCalls[0].ID != "call" || message.ToolCalls[0].Function.Name != "read_file" || message.ToolCalls[0].Function.Arguments != `{"path":"a"}` {
				t.Fatalf("tool calls=%+v", message.ToolCalls)
			}
		})
	}
}

func TestArkProviderErrorsPropagate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		responseWriter.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(responseWriter, `{"error":{"message":"invalid credentials","type":"authentication_error","code":"invalid_api_key"}}`)
	}))
	defer server.Close()
	chatModel, err := New(context.Background(), Config{Name: "test", Provider: "ark", Model: "test", BaseURL: server.URL, APIKey: "test"})
	if err != nil {
		t.Fatal(err)
	}
	messageStream, err := chatModel.Stream(context.Background(), []*schema.Message{schema.UserMessage("hello")})
	if messageStream != nil {
		messageStream.Close()
	}
	if err == nil {
		t.Fatal("provider error replaced with a successful response")
	}
}

func TestModelConfigurationNormalizationReachesProvider(t *testing.T) {
	for _, reasoningEffort := range []string{"", "   ", "LOW", "  Medium ", "high"} {
		t.Run(reasoningEffort, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
				var requestBody struct {
					Model  string `json:"model"`
					Effort string `json:"reasoning_effort"`
				}
				err := json.NewDecoder(request.Body).Decode(&requestBody)
				if err != nil {
					t.Error(err)
				}
				if requestBody.Model != "configured-model" || requestBody.Effort != strings.ToLower(strings.TrimSpace(reasoningEffort)) || request.Header.Get("Authorization") != "Bearer test-key" {
					t.Errorf("request=%+v authorization normalized=%t", requestBody, request.Header.Get("Authorization") == "Bearer test-key")
				}
				responseWriter.Header().Set("Content-Type", "application/json")
				fmt.Fprint(responseWriter, `{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
			}))
			defer server.Close()
			chatModel, err := New(context.Background(), Config{Name: " name ", Provider: " openai ", Model: " configured-model ", BaseURL: " " + server.URL + "/v1 ", APIKey: " test-key ", ReasoningEffort: reasoningEffort})
			if err != nil {
				t.Fatal(err)
			}
			message, err := chatModel.Generate(context.Background(), []*schema.Message{schema.UserMessage("hi")})
			if err != nil || message == nil || message.Content != "ok" {
				t.Fatalf("out=%v err=%v", message, err)
			}
		})
	}
}
