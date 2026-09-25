//go:build !windows

package threadhost

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"eino-cli/deepagent/dal/model"
	eventpkg "eino-cli/deepagent/protocol/event"
	inputpkg "eino-cli/deepagent/protocol/input"
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

func TestBuildRunConfigCreatesRunLocalConfig(t *testing.T) {
	host := &ThreadHost{Runtime: RuntimeConfig{
		Models: map[string]modelpkg.ToolCallingChatModel{"default": &runtimeModel{}}, DefaultModel: "default",
	}}
	info := &model.Thread{ThreadID: 42, SessionID: "session"}
	first, err := host.buildRunConfig(context.Background(), info, "", t.TempDir(), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := host.buildRunConfig(context.Background(), info, "", t.TempDir(), nil, inputpkg.UserMessageModeImplPlan)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || first.Agent.FilesystemConfig == second.Agent.FilesystemConfig {
		t.Fatal("run config must be rebuilt for every run")
	}
	if first.EnablePlan || !second.EnablePlan {
		t.Fatalf("plan mode first=%v second=%v", first.EnablePlan, second.EnablePlan)
	}
	if first.Agent.HITLConfig == nil || !first.Agent.HITLConfig.NeedFollowUpTool || second.Agent.HITLConfig == nil || !second.Agent.HITLConfig.NeedFollowUpTool {
		t.Fatal("Web runs must expose ask_user")
	}
}
