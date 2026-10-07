//go:build !windows

package threadhost

import (
	"context"
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
	threadpkg "eino-cli/deepagent/thread"

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
	runtime, err := host.createThread(ctx, &model.Thread{ThreadID: 42, SessionID: "session", Profile: &model.Profile{Cwd: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())
	output, err := runtime.Init(ctx)
	if err != nil {
		t.Fatal(err)
	}
	posted, err := runtime.PostMessage(ctx, &threadpkg.TransportMessage{ID: "101", Type: threadpkg.MessageTypeInput, Payload: []byte(`{"parts":[{"type":"text","text":"hello"}]}`), Metadata: map[string]string{"source": "test"}})
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
					err := json.Unmarshal(item.Event.Payload, &payload)
					if err != nil {
						t.Fatal(err)
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
	runtime, err := host.createThread(context.Background(), &model.Thread{ThreadID: 42, SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())
	output, err := runtime.Init(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if output == nil || output.Items == nil {
		t.Fatal("Thread.Init must initialize the output stream")
	}
	err = runtime.Close(context.Background())
	if err != nil {
		t.Fatal(err)
	}
}

type configuredHostModel struct {
	runtimeModel
	inputs [][]*schema.Message
	tools  []*schema.ToolInfo
}

func (m *configuredHostModel) WithTools(infos []*schema.ToolInfo) (modelpkg.ToolCallingChatModel, error) {
	m.tools = infos
	return m, nil
}

func (m *configuredHostModel) Stream(_ context.Context, input []*schema.Message, _ ...modelpkg.Option) (*schema.StreamReader[*schema.Message], error) {
	m.inputs = append(m.inputs, input)
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("ok", nil)}), nil
}

func TestThreadHostBuildsPromptsAndToolsForEachRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dir := t.TempDir()
	chatModel := &configuredHostModel{}
	host := &ThreadHost{Runtime: RuntimeConfig{
		Models:       map[string]modelpkg.ToolCallingChatModel{"default": chatModel},
		DefaultModel: "default", SystemPrompt: "host system prompt",
	}}
	thread, err := host.createThread(ctx, &model.Thread{
		ThreadID: 42, SessionID: "session", Profile: &model.Profile{Cwd: dir},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer thread.Close(context.Background())
	for _, instruction := range []string{"first run instruction", "second run instruction"} {
		err = os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("## Agent Working Discipline\n"+instruction), 0600)
		if err != nil {
			t.Fatal(err)
		}
		posted, err := thread.SubmitInput(ctx, schema.UserMessage("hello"))
		if err != nil {
			t.Fatal(err)
		}
		err = posted.RunHandle.Wait(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var prompt strings.Builder
		for _, message := range chatModel.inputs[len(chatModel.inputs)-1] {
			if message.Role == schema.System {
				prompt.WriteString(message.Content)
			}
		}
		if !strings.Contains(prompt.String(), instruction) || !strings.Contains(prompt.String(), "host system prompt") {
			t.Fatalf("Run lost current project or base instructions: %s", prompt.String())
		}
	}
	followUp, subagent := false, false
	for _, info := range chatModel.tools {
		followUp = followUp || info.Name == "ask_user"
		subagent = subagent || (info.Name == "task" && strings.Contains(info.Desc, "general-purpose"))
	}
	if len(chatModel.inputs) != 2 || !followUp || !subagent {
		t.Fatalf("configured capabilities missing: runs=%d ask_user=%v subagent=%v", len(chatModel.inputs), followUp, subagent)
	}
}

func TestCreateThreadDockerAllocationFailure(t *testing.T) {
	host := &ThreadHost{Runtime: RuntimeConfig{
		FilesystemKind: "docker",
		Models:         map[string]modelpkg.ToolCallingChatModel{"default": &runtimeModel{}},
		DefaultModel:   "default",
	}}
	thread, err := host.createThread(context.Background(), &model.Thread{
		ThreadID: 42, SessionID: "session", Profile: &model.Profile{Cwd: t.TempDir()},
	})
	if err == nil || thread != nil {
		t.Fatalf("expected allocation error without a Thread: thread=%v err=%v", thread, err)
	}
}

type failingConversationRepository struct{ err error }

func (s failingConversationRepository) Append(context.Context, *model.ConversationEntry) error {
	return s.err
}

func (s failingConversationRepository) LoadAfter(context.Context, string, int64, int) ([]*model.ConversationEntry, error) {
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
				// Invalid memory configuration must fail before allocating a container.
				memoryPath := filepath.Join(dir, "memory")
				err = os.WriteFile(memoryPath, []byte("not a directory"), 0600)
				if err != nil {
					t.Fatal(err)
				}
				host.Runtime.MemoryEnabled = true
				host.Runtime.MemoryDir = memoryPath
			case "init_failure":
				host.Deps.ConversationRepository = failingConversationRepository{err: historyErr}
			}
			thread, err := host.createThread(context.Background(), &model.Thread{
				ThreadID: 42, SessionID: "session", Profile: &model.Profile{Cwd: dir},
			})
			if scenario == "config_failure" {
				if err == nil || thread != nil {
					t.Fatalf("expected configuration error without a Thread: thread=%v err=%v", thread, err)
				}
				_, dockerErr := os.Stat(logPath)
				if !errors.Is(dockerErr, os.ErrNotExist) {
					t.Fatal("invalid configuration allocated a Docker container")
				}
				return
			}
			if thread == nil {
				t.Fatalf("constructed Thread must retain resource ownership: %v", err)
			}
			defer thread.Close(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			output, err := thread.Init(context.Background())
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
