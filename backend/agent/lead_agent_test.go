package agent

import (
	"testing"

	openaimodel "github.com/cloudwego/eino-ext/components/model/openai"

	"eino-cli/backend/consts"
)

func TestDefaultAgentIterations(t *testing.T) {
	if consts.DefaultAgentIterations != 50 {
		t.Fatalf("DefaultAgentIterations = %d, want 50", consts.DefaultAgentIterations)
	}
}

func TestParseReasoningEffort(t *testing.T) {
	cases := []struct {
		in   string
		want openaimodel.ReasoningEffortLevel
	}{
		{"", ""},
		{"   ", ""},
		{"unknown", ""},
		{"low", openaimodel.ReasoningEffortLevelLow},
		{"LOW", openaimodel.ReasoningEffortLevelLow},
		{"  Medium ", openaimodel.ReasoningEffortLevelMedium},
		{"high", openaimodel.ReasoningEffortLevelHigh},
	}
	for _, c := range cases {
		got := parseReasoningEffort(c.in)
		if got != c.want {
			t.Errorf("parseReasoningEffort(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
