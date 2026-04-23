package repl

import (
	"fmt"

	"eino-cli/internal/cli/render"
	"eino-cli/internal/session"
)

func approvalMessage(invocation session.ToolInvocation) render.Message {
	return render.Message{Kind: "approval", Content: fmt.Sprintf("命令 %q 需要确认，MVP 阶段暂未开放，已拒绝执行", invocation.ToolName)}
}
