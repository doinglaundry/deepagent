package session

import "time"

type TurnResult struct {
	Success bool   `json:"success"`
	Output  string `json:"output,omitempty"`
}

// Turn 代表 session 内的一次完整对话轮次（用户输入 → 模型/工具响应）。
// CompletedAt == nil 表示该轮尚未完成，是崩溃恢复的锚点。
type Turn struct {
	Index            int              `json:"index"`
	SessionID        string           `json:"session_id"`
	Input            string           `json:"input"`
	Result           TurnResult       `json:"result"`
	Invocations      []ToolInvocation `json:"invocations,omitempty"`
	AwaitingApproval bool             `json:"awaiting_approval"`
	CreatedAt        time.Time        `json:"created_at"`
	CompletedAt      *time.Time       `json:"completed_at,omitempty"`
}

func NewTurn(index int, sessionID, input string, now time.Time) Turn {
	return Turn{
		Index:     index,
		SessionID: sessionID,
		Input:     input,
		CreatedAt: now,
	}
}

func (t Turn) Complete(result TurnResult, now time.Time) Turn {
	t.Result = result
	t.CompletedAt = &now
	return t
}

func (t Turn) IsComplete() bool {
	return t.CompletedAt != nil
}
