package eino

import (
	"errors"
	"strings"

	"github.com/cloudwego/eino/adk"
)

type agentRunSummary struct {
	Output      string
	Interrupted bool
}

func collectAgentEvents(iter *adk.AsyncIterator[*adk.AgentEvent]) (agentRunSummary, error) {
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

		if trimmed := strings.TrimSpace(msg.Content); trimmed != "" {
			outputs = append(outputs, trimmed)
		}
	}

	summary.Output = strings.TrimSpace(strings.Join(outputs, "\n"))
	return summary, nil
}
