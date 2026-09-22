package agentthread

import (
	"testing"

	"github.com/cloudwego/eino/components/model"
)

func TestRunUsageAccumulatesAcrossModelCalls(t *testing.T) {
	recorder := &runEventRecorder{}
	first := recorder.accumulateModelUsage(&model.TokenUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5})
	second := recorder.accumulateModelUsage(&model.TokenUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5})
	if first.TotalTokens != 5 || second.TotalTokens != 10 || second.PromptTokens != 6 || second.CompletionTokens != 4 {
		t.Fatalf("usage snapshots = %+v, %+v", first, second)
	}
}
