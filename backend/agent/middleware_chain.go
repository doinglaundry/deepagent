package agent

import (
	"github.com/cloudwego/eino/adk"

	"eino-cli/backend/agent/middlewares"
	"eino-cli/backend/config"
)

// Chain bundles the two middleware slices that deep.New consumes.
//
// Python's _build_middlewares returns a single list because LangChain only
// has one middleware abstraction. eino splits the same surface into two
// slots: AgentMiddleware (struct-based, simple instruction/tool extras) and
// ChatModelAgentMiddleware (interface-based, full hook surface). We expose
// both so each Phase can drop the right one in.
type Chain struct {
	Agent     []adk.AgentMiddleware
	ChatModel []adk.ChatModelAgentMiddleware
}

// BuildChain mirrors _build_middlewares with the Python's "always-on"
// minimum subset for Phase 1: ToolErrorHandling, LoopDetection, and
// Clarification (last). Phase 2 will graft the rest on top.
//
// The arguments mirror the Python signature one-to-one so that wiring up
// the remaining middlewares in later phases is mechanical:
//
//   - rt:        the merged RuntimeContext (was: RunnableConfig)
//   - modelName: the resolved model name (was: model_name kwarg)
//   - agentName: the resolved agent name (was: agent_name kwarg)
//   - cfg:       the loaded application config (was: app_config)
func BuildChain(rt RuntimeContext, modelName, agentName string, cfg config.Config) Chain {
	_ = rt
	_ = modelName
	_ = agentName
	_ = cfg

	return Chain{
		Agent: nil,
		ChatModel: []adk.ChatModelAgentMiddleware{
			middlewares.NewToolErrorHandling(),
			middlewares.NewLoopDetection(),
			// Clarification stays last — same invariant as Python.
			middlewares.NewClarification(),
		},
	}
}
