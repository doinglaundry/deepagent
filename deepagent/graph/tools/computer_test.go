package tools

import (
	"context"
	"testing"

	"eino-cli/deepagent/graph/computer"
	agentmodel "eino-cli/deepagent/model"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func TestComputerTools_ContractAndChildIsolation(t *testing.T) {
	descriptors := append(NewBrowserTools(&computer.Browser{}), NewComputerTools(&computer.Desktop{})...)
	if len(descriptors) != 12 {
		t.Fatal(len(descriptors))
	}
	set, err := NewToolSet(context.Background(), descriptors)
	if err != nil {
		t.Fatal(err)
	}
	infos, err := set.GetToolInfos(context.Background())
	if err != nil || len(infos) != 12 {
		t.Fatalf("schemas: %v %v", infos, err)
	}
	for _, descriptor := range descriptors {
		info, err := descriptor.Tool.Info(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		observe := info.Name == "browser_observe" || info.Name == "computer_observe"
		if descriptor.ReadOnly != observe || descriptor.RequiresApproval == observe || descriptor.ParallelSafe {
			t.Fatalf("capabilities: %+v", descriptor)
		}
		enhanced, ok := descriptor.Tool.(tool.EnhancedInvokableTool)
		if !ok {
			t.Fatal("missing image tool interface")
		}
		ctx := agentmodel.WithRunState(context.Background(), &agentmodel.RunState{Depth: 1, RunID: "child"})
		_, err = enhanced.InvokableRun(ctx, &schema.ToolArgument{Text: `{}`})
		if err == nil {
			t.Fatal("child was allowed desktop access")
		}
	}
}
