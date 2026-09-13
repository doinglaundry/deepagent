package agentthread

import (
	"context"
	"eino-cli/deepagent/core/graph"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

func copyConsumedInputsMeta(in []any) []any {
	if len(in) == 0 {
		return nil
	}
	hasValue := false
	for _, meta := range in {
		if meta != nil {
			hasValue = true
			break
		}
	}
	if !hasValue {
		return nil
	}
	out := make([]any, len(in))
	copy(out, in)
	return out
}
func defaultRunIDProvider(context.Context, string, *Message) string { return uuid.NewString() }
func copyResumeRunOptions(in *ResumeRunOptions) *ResumeRunOptions {
	if in == nil {
		return nil
	}
	out := *in
	if len(in.ResumeInterruptIDs) > 0 {
		out.ResumeInterruptIDs = append([]string(nil), in.ResumeInterruptIDs...)
	}
	if len(in.ResumeData) > 0 {
		out.ResumeData = make(map[string]any, len(in.ResumeData))
		for k, v := range in.ResumeData {
			out.ResumeData[k] = v
		}
	}
	return &out
}
func copyMessages(msgs []*schema.Message) []*schema.Message {
	if len(msgs) == 0 {
		return nil
	}
	out := make([]*schema.Message, len(msgs))
	for i, msg := range msgs {
		out[i] = graph.CopyMessage(msg)
	}
	return out
}
