package agent

import (
	"testing"

	openaimodel "github.com/cloudwego/eino-ext/components/model/openai"
)

// TestParseReasoningEffort exercises the textual → typed mapping the
// OpenAI client expects. Empty / unknown inputs must fall through as
// the zero value so the upstream default applies.
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
