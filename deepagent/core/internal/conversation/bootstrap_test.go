package conversation

import (
	"context"
	"github.com/cloudwego/eino/schema"
	"testing"
)

func TestBootstrapReplacementPreservesSummaryAndHistoricalSource(t *testing.T) {
	for _, tc := range []struct {
		name, first         string
		enabled, withPrompt bool
		wantCount           int
	}{
		{"replace stale bootstrap", "old prompt", true, true, 2},
		{"preserve leading summary", "Earlier conversation summary: important", true, true, 3},
		{"default keeps system history", "historical instruction", false, true, 3},
		{"no replacement prompt keeps history", "old prompt", true, false, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			c := New("thread", nil, nil, nil, WithBootstrapPromptReplacement(tc.enabled))
			original := schema.SystemMessage(tc.first)
			if err := c.AddHistory(ctx, "old", original, schema.UserMessage("prior")); err != nil {
				t.Fatal(err)
			}
			var prompts []*schema.Message
			if tc.withPrompt {
				prompts = []*schema.Message{schema.SystemMessage("fresh prompt")}
			}
			request, err := c.BuildRequest(ctx, prompts)
			if err != nil {
				t.Fatal(err)
			}
			if len(request) != tc.wantCount {
				t.Fatalf("request=%v", request)
			}
			if tc.withPrompt && request[0].Content != "fresh prompt" {
				t.Fatal("fresh prompt lost")
			}
			if tc.name == "preserve leading summary" && request[1].Content != tc.first {
				t.Fatal("summary discarded")
			}
			history := c.History(ctx)
			if len(history) != 2 || history[0] != original || original.Content != tc.first {
				t.Fatal("request projection mutated source history")
			}
		})
	}
}
