package tools

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/components/tool"
)

func TestLocalModelToolRejectsInvalidArguments(t *testing.T) {
	// Invalid arguments must fail before accessing the service.
	toolDescriptor := NewLocalModelTool(nil)
	if !toolDescriptor.ReadOnly || toolDescriptor.ParallelSafe || toolDescriptor.ReturnDirect {
		t.Fatalf("unexpected tool policy: %+v", toolDescriptor)
	}
	toolInfo, err := toolDescriptor.Tool.Info(context.Background())
	if err != nil || toolInfo.Name != "ask_local_model" {
		t.Fatalf("info=%+v err=%v", toolInfo, err)
	}
	invokableTool := toolDescriptor.Tool.(tool.InvokableTool)
	for _, argumentsJSON := range []string{`{`, `{}`, `{"prompt":" "}`} {
		_, err := invokableTool.InvokableRun(context.Background(), argumentsJSON)
		if err == nil {
			t.Fatalf("invalid arguments accepted: %s", argumentsJSON)
		}
	}
}
