package repl

import (
	"fmt"

	"eino-cli/internal/cli/render"
	"eino-cli/internal/session"
)

func approvalMessage(invocation session.ToolInvocation) render.Message {
	return render.Message{Kind: "approval", Content: fmt.Sprintf("命令 %q 需要确认，请输入 y/yes 批准，其他输入视为拒绝", invocation.ToolName)}
}
