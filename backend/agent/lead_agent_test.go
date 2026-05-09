package agent

import (
	"testing"

	openaimodel "github.com/cloudwego/eino-ext/components/model/openai"

	"eino-cli/backend/config"
)

func TestDefaultIterationLimit(t *testing.T) {
	cases := []struct {
		name    string
		profile *config.AgentConfig
		want    int
	}{
		{"nil profile", nil, 6},
		{"zero falls back", &config.AgentConfig{MaxIteration: 0}, 6},
		{"negative falls back", &config.AgentConfig{MaxIteration: -1}, 6},
		{"explicit override", &config.AgentConfig{MaxIteration: 12}, 12},
		{"large override", &config.AgentConfig{MaxIteration: 100}, 100},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := defaultIterationLimit(c.profile); got != c.want {
				t.Errorf("defaultIterationLimit() = %d, want %d", got, c.want)
			}
		})
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
