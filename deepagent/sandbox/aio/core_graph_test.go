package aio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"eino-cli/deepagent/core/backend"
	"eino-cli/deepagent/core/graph"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type sandboxGraphModel struct {
	readOnly bool
	calls    int
	tools    map[string]bool
	inputs   [][]*schema.Message
}

type staticPathSandbox struct{ *Sandbox }

func (*staticPathSandbox) ResolveContainerPath(_ context.Context, path string) (string, error) {
	return path, nil
}

func (m *sandboxGraphModel) WithTools(infos []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	m.tools = map[string]bool{}
	for _, info := range infos {
		m.tools[info.Name] = true
	}
	return m, nil
}
func (m *sandboxGraphModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, fmt.Errorf("unexpected non-stream model call")
}
func (m *sandboxGraphModel) Stream(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.inputs = append(m.inputs, append([]*schema.Message(nil), input...))
	m.calls++
	var msg *schema.Message
	switch {
	case m.calls == 1:
		msg = schema.AssistantMessage("", []schema.ToolCall{{ID: "read", Function: schema.FunctionCall{Name: "read_file", Arguments: `{"path":"a.txt"}`}}})
	case m.calls == 2 && !m.readOnly:
		msg = schema.AssistantMessage("", []schema.ToolCall{{ID: "edit", Function: schema.FunctionCall{Name: "edit_file", Arguments: `{"path":"a.txt","old":"original","new":"updated"}`}}})
	default:
		msg = schema.AssistantMessage("done", nil)
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func TestCoreGraphUsesDockerWorkspaceTools(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		t.Run(fmt.Sprint(readOnly), func(t *testing.T) {
			var mu sync.Mutex
			content := "original"
			var requests []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				var body struct {
					File    string `json:"file"`
					Content string `json:"content"`
					Append  bool   `json:"append"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					http.Error(w, "bad JSON", 400)
					return
				}
				if r.Method != http.MethodPost || body.File != "/virtual/a.txt" {
					t.Errorf("request=%s %s file=%s", r.Method, r.URL.Path, body.File)
					http.Error(w, "bad request", 400)
					return
				}
				requests = append(requests, r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/v1/file/read":
					_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]string{"content": content}})
				case "/v1/file/write":
					if readOnly || body.Append {
						t.Error("unexpected mutation mode")
					}
					content = body.Content
					_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{}})
				default:
					t.Errorf("unexpected provider endpoint: %s", r.URL.Path)
					http.Error(w, "unexpected", 400)
				}
			}))
			defer server.Close()
			provider := newSandbox("sandbox", "thread", server.URL, nil)
			provider.containerName, provider.runtime = "test-container", runtimeDocker
			files, err := backend.NewDockerFilesystem(&staticPathSandbox{provider}, "/virtual", "thread")
			if err != nil {
				t.Fatal(err)
			}
			m := &sandboxGraphModel{readOnly: readOnly}
			agent, err := graph.New(context.Background(), graph.WithConfig(&graph.Config{ThreadID: "thread", Model: m, Filesystem: files, FilesystemConfig: &graph.FilesystemConfig{ReadOnly: readOnly}, Policy: tools.PolicyFunc(func(context.Context, types.ToolCall, tools.Descriptor) (tools.Decision, error) {
				return tools.Decision{Action: tools.Allow}, nil
			})}))
			if err != nil {
				t.Fatal(err)
			}
			defer agent.Close(context.Background())
			result, err := agent.Run(context.Background(), []*schema.Message{schema.UserMessage("inspect file")})
			if err != nil || result.Content != "done" {
				t.Fatalf("result=%v err=%v", result, err)
			}
			for _, name := range []string{"execute", "shell", "await_shell", "read_lints"} {
				if m.tools[name] == readOnly {
					t.Fatalf("Docker tool %s available=%v readOnly=%v", name, m.tools[name], readOnly)
				}
			}
			if !m.tools["read_file"] || m.tools["edit_file"] == readOnly {
				t.Fatalf("unexpected tools %v", m.tools)
			}
			last := m.inputs[1][len(m.inputs[1])-1]
			if last.Role != schema.Tool || last.ToolCallID != "read" || !strings.Contains(last.Content, "original") {
				t.Fatalf("provider response missing from next model request: %+v", last)
			}
			mu.Lock()
			defer mu.Unlock()
			wantRequests, wantCalls, wantContent := 3, 3, "updated"
			if readOnly {
				wantRequests, wantCalls, wantContent = 1, 2, "original"
			}
			if len(requests) != wantRequests || m.calls != wantCalls || content != wantContent {
				t.Fatalf("requests=%v model=%d content=%s", requests, m.calls, content)
			}
		})
	}
}

func TestCoreDockerFilesystemPreservesInFlightCancellation(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	provider := newSandbox("sandbox", "thread", server.URL, nil)
	provider.containerName, provider.runtime = "test-container", runtimeDocker
	files, err := backend.NewDockerFilesystem(&staticPathSandbox{provider}, "/virtual", "thread")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := files.Read(ctx, "a.txt", nil, nil); result <- err }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("provider was not called")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("provider read did not stop")
	}
}
