package middlewares

import (
	"context"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

const planModeTag = "<plan_mode>"

type PlanReminder struct {
	*adk.BaseChatModelAgentMiddleware
	getPlanModeFunc func() bool
}

func NewPlanReminder(getPlanModeFunc func() bool) *PlanReminder {
	return &PlanReminder{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		getPlanModeFunc:              getPlanModeFunc,
	}
}

func (m *PlanReminder) BeforeModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	_ *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if state == nil || m.getPlanModeFunc == nil || !m.getPlanModeFunc() {
		return ctx, state, nil
	}
	state.Messages = appendPlanInstruction(state.Messages)
	return ctx, state, nil
}

func appendPlanInstruction(msgs []*schema.Message) []*schema.Message {
	if len(msgs) == 0 || msgs[0] == nil || msgs[0].Role != schema.System {
		return msgs
	}
	if strings.Contains(msgs[0].Content, planModeTag) {
		return msgs
	}
	cloned := *msgs[0]
	cloned.Content += TodoInstruction
	out := make([]*schema.Message, len(msgs))
	out[0] = &cloned
	copy(out[1:], msgs[1:])
	return out
}
