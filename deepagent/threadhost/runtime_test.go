//go:build !windows

package threadhost

import (
	"context"
	"errors"
	"testing"

	"eino-cli/deepagent/dal/model"
	inputpkg "eino-cli/deepagent/protocol/input"
	modelpkg "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type runtimeModel struct{}

func (*runtimeModel) WithTools([]*schema.ToolInfo) (modelpkg.ToolCallingChatModel, error) {
	return &runtimeModel{}, nil
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
}
