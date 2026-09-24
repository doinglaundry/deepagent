package graph

import "github.com/cloudwego/eino/schema"

// Retain historical checkpoint type names for decoding only.
// Execution and tool accounting belong to toolExecutor in tools.go.
type streamToolExecutorInterruptInfo struct {
	CompletedCallIDs   []string
	InterruptedCallIDs []string
}

type streamToolExecutorInterruptState struct {
	ToolCalls      []schema.ToolCall
	CompletedCalls map[string][]*schema.Message
}

func init() {
	schema.RegisterName[*streamToolExecutorInterruptInfo]("_deepagents_stream_tool_executor_interrupt_info")
	schema.RegisterName[*streamToolExecutorInterruptState]("_deepagents_stream_tool_executor_interrupt_state")
}
