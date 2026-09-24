package graph

import (
	"context"
	"fmt"

	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
)

// Legacy configuration is translated at the boundary. The executor still
// performs one policy decision before invoking either tool protocol.
func policyWithGates(fallback tools.Policy, gates map[string]tools.ToolPolicyGate) tools.Policy {
	return tools.PolicyFunc(func(ctx context.Context, call types.ToolCall, descriptor tools.Descriptor) (tools.Decision, error) {
		base := tools.Decision{Action: tools.Allow}
		if descriptor.RequiresApproval {
			base.Action = tools.AskApproval
		}
		if fallback != nil {
			var err error
			base, err = fallback.Decide(ctx, call, descriptor)
			if err != nil || base.Action == tools.Deny {
				return base, err
			}
			if base.Action != tools.Allow && base.Action != tools.AskApproval {
				return tools.Decision{}, fmt.Errorf("invalid policy action %q", base.Action)
			}
		}
		gate, exists := gates[call.Name]
		if !exists {
			info, err := descriptor.Tool.Info(ctx)
			if err != nil {
				return tools.Decision{}, err
			}
			if info != nil {
				gate, exists = gates[info.Name]
			}
		}
		if exists {
			if gate.Policy == nil {
				return tools.Decision{}, fmt.Errorf("tool %q policy gate requires Policy", call.Name)
			}
			info := &tools.ApprovalInfo{CallID: call.ID, ToolName: call.Name, Arguments: call.Arguments, ArgumentsInJSON: call.Arguments}
			decision, err := gate.Policy(ctx, info)
			if err != nil {
				return tools.Decision{}, err
			}
			switch decision.Action {
			case tools.ToolCallDeny:
				if gate.DenyFormatter != nil {
					content, err := gate.DenyFormatter(ctx, info, decision)
					if err != nil {
						return tools.Decision{}, err
					}
					decision.Reason = content
				}
				return tools.Decision{Action: tools.Deny, Reason: decision.Reason}, nil
			case string(tools.AskApproval):
				return tools.Decision{Action: tools.AskApproval, Reason: decision.Reason}, nil
			case "", string(tools.Allow):
			default:
				return tools.Decision{}, fmt.Errorf("invalid policy action %q", decision.Action)
			}
		}
		return base, nil
	})
}
