package middlewares

import (
	"context"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// planModeTag is the idempotency anchor: TodoInstruction already wraps
// itself in <plan_mode>...</plan_mode>, so a retry / interrupt-resume
// pass that sees this tag in msgs[0] knows the append already happened.
const planModeTag = "<plan_mode>"

// PlanReminder appends TodoInstruction onto the existing system message
// when getOn() returns true. String content is identical to the
// build-time AdditionalInstruction path it replaces, so the model
// observes no behavioural difference. Toggling = atomic.Bool flip on
// the runtime side; no agent rebuild.
type PlanReminder struct {
	*adk.BaseChatModelAgentMiddleware
	getOn func() bool
}

func NewPlanReminder(getOn func() bool) *PlanReminder {
	return &PlanReminder{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		getOn:                        getOn,
	}
}

func (m *PlanReminder) BeforeModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	_ *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if state == nil || m.getOn == nil || !m.getOn() {
		return ctx, state, nil
	}
	state.Messages = appendPlanInstruction(state.Messages)
	return ctx, state, nil
}

// appendPlanInstruction targets msgs[0] when it's the agent's system
// instruction; idempotent via planModeTag. Returns msgs unchanged when
// the tag is already present (replay / interrupt-resume) or msgs[0]
// isn't a system message (defensive — eino agent always seeds one).
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
