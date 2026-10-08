// Package message defines the dialogue message used by Thread, Run and Graph.
package message

import (
	inputpkg "eino-cli/deepagent/protocol/input"

	"github.com/cloudwego/eino/schema"
)

// Message 同时用于业务逻辑和对话持久化。Eino Message 只在模型边界转换。
// 多模态、工具调用和用量沿用 Eino 的字段类型，避免再定义一套协议。
type Message struct {
	MessageID     string                 `json:"message_id,omitempty"`
	ThreadID      string                 `json:"thread_id,omitempty"`
	RunID         string                 `json:"run_id,omitempty"`
	Seq           int64                  `json:"seq,omitempty"`
	CreatedAt     int64                  `json:"created_at,omitempty"`
	SenderID      string                 `json:"sender_id,omitempty"`
	SenderType    string                 `json:"sender_type,omitempty"`
	OriginalParts []inputpkg.MessagePart `json:"original_parts,omitempty"`

	Role                     schema.RoleType            `json:"role"`
	Content                  string                     `json:"content"`
	MultiContent             []schema.ChatMessagePart   `json:"multi_content,omitempty"`
	UserInputMultiContent    []schema.MessageInputPart  `json:"user_input_multi_content,omitempty"`
	AssistantGenMultiContent []schema.MessageOutputPart `json:"assistant_output_multi_content,omitempty"`
	Name                     string                     `json:"name,omitempty"`
	ToolCalls                []schema.ToolCall          `json:"tool_calls,omitempty"`
	ToolCallID               string                     `json:"tool_call_id,omitempty"`
	ToolName                 string                     `json:"tool_name,omitempty"`
	ResponseMeta             *schema.ResponseMeta       `json:"response_meta,omitempty"`
	ReasoningContent         string                     `json:"reasoning_content,omitempty"`
	Extra                    map[string]any             `json:"extra,omitempty"`
}

func init() { schema.RegisterName[*Message]("deepagent_message") }

func NewUserMessage(content string) *Message { return &Message{Role: schema.User, Content: content} }
func NewSystemMessage(content string) *Message {
	return &Message{Role: schema.System, Content: content}
}
func NewAssistantMessage(content string, calls []schema.ToolCall) *Message {
	return &Message{Role: schema.Assistant, Content: content, ToolCalls: calls}
}
func NewToolMessage(content, callID string) *Message {
	return &Message{Role: schema.Tool, Content: content, ToolCallID: callID}
}
