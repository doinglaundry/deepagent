package deepagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"eino-cli/backend/consts"
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
		if msg.Role != schema.Assistant {
			continue
		}
		if clarification := getClarificationOutput(msg); clarification != "" {
			if onChunk != nil {
				onChunk(clarification)
			}
			outputs = append(outputs, clarification)
			continue
		}
		if len(msg.ToolCalls) > 0 {
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

func getClarificationOutput(msg *schema.Message) string {
	for _, call := range msg.ToolCalls {
		if call.Function.Name == consts.AskClarificationToolName {
			return parseClarificationOutput(call.Function.Arguments)
		}
	}
	return ""
}

func parseClarificationOutput(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var args struct {
		Question string          `json:"question"`
		Prompt   string          `json:"prompt"`
		Message  string          `json:"message"`
		Context  string          `json:"context"`
		Options  json.RawMessage `json:"options"`
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return fmt.Sprintf("(unparsed clarification args: %s)", raw)
	}
	question := getClarificationQuestion(args.Question, args.Prompt, args.Message)
	if question == "" {
		return strings.TrimSpace(raw)
	}
	var parts []string
	if context := strings.TrimSpace(args.Context); context != "" {
		parts = append(parts, context, "")
	}
	parts = append(parts, question)
	if options := parseClarificationOptions(args.Options); len(options) > 0 {
		parts = append(parts, "")
		for i, option := range options {
			parts = append(parts, fmt.Sprintf("%d. %s", i+1, option))
		}
	}
	return strings.Join(parts, "\n")
}

func getClarificationQuestion(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func parseClarificationOptions(raw json.RawMessage) []string {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return nil
	}
	var options []string
	if err := json.Unmarshal(raw, &options); err == nil {
		return cleanClarificationOptions(options)
	}
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return nil
	}
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(encoded), &options); err == nil {
		return cleanClarificationOptions(options)
	}
	return cleanClarificationOptions([]string{encoded})
}

func cleanClarificationOptions(options []string) []string {
	out := make([]string, 0, len(options))
	for _, option := range options {
		if trimmed := strings.TrimSpace(option); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
