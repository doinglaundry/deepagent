package deepagent

import (
	"errors"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	rt "eino-cli/backend/runtime"
)

type agentRunSummary struct {
	Output      string
	Interrupted bool
}

func collectAgentEventsWithSink(iter *adk.AsyncIterator[*adk.AgentEvent], onChunk rt.StreamChunkHandler) (agentRunSummary, error) {
	var summary agentRunSummary
	var outputs []string

	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event == nil {
			continue
		}
		if event.Err != nil {
			var cancelErr *adk.CancelError
			if errors.As(event.Err, &cancelErr) {
				summary.Interrupted = true
				continue
			}
			return agentRunSummary{}, event.Err
		}

		if event.Action != nil && event.Action.Interrupted != nil {
			summary.Interrupted = true
			continue
		}

		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}

		msg, err := event.Output.MessageOutput.GetMessage()
		if err != nil || msg == nil {
			continue
		}
		if msg.Role != schema.Assistant || len(msg.ToolCalls) > 0 {
			continue
		}

		if onChunk != nil && msg.Content != "" {
			onChunk(msg.Content)
		}
		if trimmed := strings.TrimSpace(msg.Content); trimmed != "" {
			outputs = append(outputs, trimmed)
		}
	}

	summary.Output = strings.Join(outputs, "\n")
	return summary, nil
}
