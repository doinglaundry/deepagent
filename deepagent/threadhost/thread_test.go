//go:build !windows

package threadhost

import (
	"context"
	deepagents "eino-cli/deepagent/core"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"eino-cli/deepagent/config"
	"eino-cli/deepagent/dal/model"
	eventpkg "eino-cli/deepagent/protocol/event"

	modelpkg "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type runtimeModel struct{}

func (*runtimeModel) WithTools([]*schema.ToolInfo) (modelpkg.ToolCallingChatModel, error) {
	return &runtimeModel{}, nil
}

func TestThreadHostCanonicalRuntimeSubmitToYield(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	host := &ThreadHost{Runtime: RuntimeConfig{Models: map[string]modelpkg.ToolCallingChatModel{"default": &runtimeModel{}}, DefaultModel: "default"}}
	runtime, output, err := host.createThread(ctx, &model.Thread{ThreadID: 42, SessionID: "session", Profile: &model.Profile{Cwd: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())
	posted, err := runtime.PostMessage(ctx, &deepagents.TransportMessage{ID: "101", Type: deepagents.MessageTypeInput, Payload: []byte(`{"parts":[{"type":"text","text":"hello"}]}`), Metadata: map[string]string{"source": "test"}})
	if err != nil {
		t.Fatal(err)
	}
	if posted.RunID == "" {
		t.Fatal("missing accepted run identity")
	}
	assistant := false
	for {
		select {
		case item, ok := <-output.Items:
			if !ok {
				t.Fatal("output closed before yield")
			}
			if item.Event != nil {
				if item.Event.RunID != posted.RunID {
					t.Fatalf("run identity changed: %+v", item.Event)
				}
				if string(item.Event.Type) == eventpkg.EventTypeError.String() {
					t.Fatalf("agent error: %s", item.Event.Payload)
				}
				if string(item.Event.Type) == eventpkg.EventTypeAssistantMessage.String() {
					var payload eventpkg.MessageEventPayload
					{
						err := json.Unmarshal(item.Event.Payload, &payload)
						if err != nil {
							t.Fatal(err)
						}
					}
					if len(payload.Parts) == 0 {
						t.Fatal("assistant message missing content")
					}
					assistant = true
				}
			}
			if item.Yield != nil {
				if !assistant || item.Yield.Reason != "finished" || item.Yield.Err != nil {
					t.Fatalf("yield=%+v assistant=%v", item.Yield, assistant)
				}
				return
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
func (*runtimeModel) Generate(context.Context, []*schema.Message, ...modelpkg.Option) (*schema.Message, error) {
	return nil, errors.New("not used")
}
func (*runtimeModel) Stream(context.Context, []*schema.Message, ...modelpkg.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("ok", nil)}), nil
}

func TestThreadHostCreatesCanonicalRuntime(t *testing.T) {
	host := &ThreadHost{Runtime: RuntimeConfig{
		Models: map[string]modelpkg.ToolCallingChatModel{"default": &runtimeModel{}}, DefaultModel: "default",
	}}
	runtime, output, err := host.createThread(context.Background(), &model.Thread{ThreadID: 42, SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	if output == nil || output.Items == nil {
		t.Fatal("createThread must initialize the output stream")
	}
	err = runtime.Close(context.Background())
	if err != nil {
		t.Fatal(err)
	}
}

func TestBuildRunConfigCreatesRunLocalMiddlewares(t *testing.T) {
	host := &ThreadHost{Runtime: RuntimeConfig{
		Models: map[string]modelpkg.ToolCallingChatModel{"default": &runtimeModel{}}, DefaultModel: "default",
	}}
	info := &model.Thread{ThreadID: 42, SessionID: "session"}
	cfg, err := host.buildRunConfig(info, host.Runtime.Models["default"], nil)
	if err != nil {
		t.Fatal(err)
	}
	firstMiddlewares := cfg.MiddlewaresProvider(context.Background(), "first")
	secondMiddlewares := cfg.MiddlewaresProvider(context.Background(), "second")
	if len(firstMiddlewares) == 0 || len(secondMiddlewares) != len(firstMiddlewares) {
		t.Fatal("each run must receive its middleware instances")
	}
	for i := range firstMiddlewares {
		if firstMiddlewares[i] == secondMiddlewares[i] {
			t.Fatal("mutable middleware must be created for each run")
		}
	}
	found := false
	for _, descriptor := range cfg.Agent.ToolDescriptors {
		info, err := descriptor.Tool.Info(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		found = found || info.Name == "ask_user"
	}
	if !found {
		t.Fatal("Web runs must expose ask_user")
	}
	if len(cfg.Agent.SubAgents) != 1 || cfg.Agent.SubAgents[0].Name != "general-purpose" {
		t.Fatal("Web must explicitly configure its default child")
	}
}

func TestCreateThreadDockerAllocationFailure(t *testing.T) {
	host := &ThreadHost{Runtime: RuntimeConfig{
		FilesystemKind: "docker",
		Models:         map[string]modelpkg.ToolCallingChatModel{"default": &runtimeModel{}},
		DefaultModel:   "default",
	}}
	thread, output, err := host.createThread(context.Background(), &model.Thread{
		ThreadID: 42, SessionID: "session", Profile: &model.Profile{Cwd: t.TempDir()},
	})
	if err == nil || thread != nil || output != nil {
		t.Fatalf("expected allocation error without a Thread: thread=%v output=%v err=%v", thread, output, err)
	}
}

type failingHistoryStore struct{ err error }

func (s failingHistoryStore) Append(context.Context, *deepagents.HistoryRecord) error {
	return s.err
}

func (s failingHistoryStore) List(context.Context, deepagents.ListQuery) ([]*deepagents.HistoryRecord, error) {
	return nil, s.err
}

func TestCreateThreadDockerResourceOwnership(t *testing.T) {
	for _, scenario := range []string{"success", "config_failure", "init_failure"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			logPath := filepath.Join(dir, "docker.log")
			// Substitute only the external Docker CLI; use the real filesystem and Thread.
			script := `#!/bin/sh
printf '%s\n' "$*" >> "$DOCKER_LOG"
case "$1" in
inspect) printf '%s\n' '[{"NetworkSettings":{"Ports":{"8080/tcp":[{"HostPort":"18080"}]}},"Created":"2026-01-01T00:00:00Z"}]' ;;
rm) ;;
*) exit 1 ;;
esac
`
			err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir)
			t.Setenv("DOCKER_LOG", logPath)
			cleanupCount := func() int {
				data, readErr := os.ReadFile(logPath)
				if readErr != nil {
					t.Fatal(readErr)
				}
				return strings.Count(string(data), "rm -f ")
			}
			host := &ThreadHost{Runtime: RuntimeConfig{
				FilesystemKind: "docker", Docker: config.SandboxConfig{Image: "test-image"},
				Models: map[string]modelpkg.ToolCallingChatModel{"default": &runtimeModel{}}, DefaultModel: "default",
			}}
			historyErr := errors.New("history unavailable")
			switch scenario {
			case "config_failure":
				// A file cannot be used as a memory directory; config fails after allocation.
				host.Runtime.MemoryEnabled = true
				host.Runtime.MemoryDir = logPath
			case "init_failure":
				host.Deps.History = failingHistoryStore{err: historyErr}
			}
			thread, output, err := host.createThread(context.Background(), &model.Thread{
				ThreadID: 42, SessionID: "session", Profile: &model.Profile{Cwd: dir},
			})
			if scenario == "config_failure" {
				if err == nil || thread != nil || output != nil || cleanupCount() != 1 {
					t.Fatalf("construction failure leaked resources: thread=%v output=%v err=%v", thread, output, err)
				}
				return
			}
			if thread == nil {
				t.Fatalf("constructed Thread must retain resource ownership: %v", err)
			}
			defer thread.Close(context.Background())
			if scenario == "init_failure" {
				if !errors.Is(err, historyErr) || output != nil {
					t.Fatalf("lost initialization failure: output=%v err=%v", output, err)
				}
			} else if err != nil || output == nil || output.Items == nil {
				t.Fatalf("Thread was not initialized: output=%v err=%v", output, err)
			}
			if cleanupCount() != 0 {
				t.Fatal("container released before Thread.Close")
			}
			for i := 0; i < 2; i++ {
				err = thread.Close(context.Background())
				if err != nil {
					t.Fatal(err)
				}
			}
			if cleanupCount() != 1 {
				t.Fatal("Thread.Close must release the container once")
			}
		})
	}
}
