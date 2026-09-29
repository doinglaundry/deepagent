//go:build !windows

package threadhost

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"eino-cli/deepagent/core/backend"
	"eino-cli/deepagent/dal/model"
	eventpkg "eino-cli/deepagent/protocol/event"
	threadpkg "eino-cli/deepagent/thread"
	modelpkg "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type filesystemCloseProbe struct {
	backend.ToolFilesystem
	closeCalls int
}

func (f *filesystemCloseProbe) Close(context.Context) error {
	f.closeCalls++
	return nil
}

type runtimeCloseProbe struct {
	closeErr error
}

func (*runtimeCloseProbe) Init(context.Context) (*threadpkg.TransportThreadOutput, error) {
	return nil, nil
}

func (*runtimeCloseProbe) PostMessage(context.Context, *threadpkg.TransportMessage) (*threadpkg.TransportPostMessageResult, error) {
	return nil, nil
}

func (*runtimeCloseProbe) Interrupt(context.Context, threadpkg.TransportThreadInterruptRequest) error {
	return nil
}

func (*runtimeCloseProbe) ActiveRun() *threadpkg.TransportActiveRun {
	return nil
}

func (r *runtimeCloseProbe) Close(context.Context) error {
	return r.closeErr
}

func TestFilesystemThreadDoesNotCloseFilesystemAfterRuntimeCloseFailure(t *testing.T) {
	filesystem := &filesystemCloseProbe{}
	cleanupCalls := 0
	runtime := &runtimeCloseProbe{closeErr: errors.New("runtime close failed")}
	thread := &filesystemThread{
		ThreadRuntime: runtime,
		filesystem:    filesystem,
		cleanup: func() {
			cleanupCalls++
		},
	}
	err := thread.Close(context.Background())
	if err == nil || !errors.Is(err, runtime.closeErr) {
		t.Fatalf("close error=%v", err)
	}
	if filesystem.closeCalls != 0 || cleanupCalls != 0 {
		t.Fatalf("filesystem close calls=%d cleanup calls=%d", filesystem.closeCalls, cleanupCalls)
	}
}

type runtimeModel struct{}

func (*runtimeModel) WithTools([]*schema.ToolInfo) (modelpkg.ToolCallingChatModel, error) {
	return &runtimeModel{}, nil
}

func TestThreadHostCanonicalRuntimeSubmitToYield(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	host := &ThreadHost{Runtime: RuntimeConfig{Models: map[string]modelpkg.ToolCallingChatModel{"default": &runtimeModel{}}, DefaultModel: "default"}}
	runtime, err := host.createDeepAgentThread(ctx, &model.Thread{ThreadID: 42, SessionID: "session", Profile: &model.Profile{Cwd: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	output, err := runtime.Init(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())
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
					if err := json.Unmarshal(item.Event.Payload, &payload); err != nil {
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

func TestThreadHostCreatesCanonicalRuntimeWithoutFactory(t *testing.T) {
	host := &ThreadHost{Runtime: RuntimeConfig{
		Models: map[string]modelpkg.ToolCallingChatModel{"default": &runtimeModel{}}, DefaultModel: "default",
	}}
	runtime, err := host.createDeepAgentThread(context.Background(), &model.Thread{ThreadID: 42, SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = runtime.Close(context.Background()); err != nil {
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
