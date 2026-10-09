// Package model 集中共享数据、能力接口及类型自身的方法，不依赖实现包。
package model

import (
	"encoding/json"
	"errors"

	"github.com/cloudwego/eino/schema"
)

const (
	MessageTypeInput   = "input"
	MessageTypeResume  = "resume_run"
	MessageTypeCompact = "compact"
	MetadataRunMode    = "run_mode"
	RunModePlan        = "plan"
)

type UserMessageMode string

const UserMessageModeImplPlan UserMessageMode = "plan"

type InputMessagePartType string

const (
	InputMessagePartTypeText  InputMessagePartType = "text"
	InputMessagePartTypeImage InputMessagePartType = "image"
	InputMessagePartTypeAudio InputMessagePartType = "audio"
	InputMessagePartTypeVideo InputMessagePartType = "video"
	InputMessagePartTypeFile  InputMessagePartType = "file"
)

type InputMessagePart struct {
	Type       InputMessagePartType       `json:"type"`
	Text       string                     `json:"text,omitempty"`
	URL        string                     `json:"url,omitempty"`
	MIMEType   string                     `json:"mime_type,omitempty"`
	Base64Data string                     `json:"base64_data,omitempty"`
	Detail     string                     `json:"detail,omitempty"`
	Name       string                     `json:"name,omitempty"`
	Extra      map[string]json.RawMessage `json:"extra,omitempty"`
}

type UserMessage struct {
	Mode  UserMessageMode            `json:"mode,omitempty"`
	Parts []InputMessagePart         `json:"parts"`
	Extra map[string]json.RawMessage `json:"extra,omitempty"`
}

// Message 同时用于业务逻辑和对话持久化。Eino Message 只在模型边界转换。
// 多模态、工具调用和用量沿用 Eino 的字段类型，避免再定义一套协议。
type Message struct {
	MessageID     string             `json:"message_id,omitempty"`
	ThreadID      string             `json:"thread_id,omitempty"`
	RunID         string             `json:"run_id,omitempty"`
	Seq           int64              `json:"seq,omitempty"`
	CreatedAt     int64              `json:"created_at,omitempty"`
	SenderID      string             `json:"sender_id,omitempty"`
	SenderType    string             `json:"sender_type,omitempty"`
	OriginalParts []InputMessagePart `json:"original_parts,omitempty"`

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

func (m UserMessage) Validate() error {
	if len(m.Parts) == 0 {
		return errors.New("user message parts are required")
	}
	return nil
}

// FromEino 接收模型输出；模型不负责业务消息身份。
func FromEino(source *schema.Message) *Message {
	if source == nil {
		return nil
	}
	return &Message{
		Role: source.Role, Content: source.Content, MultiContent: source.MultiContent,
		UserInputMultiContent: source.UserInputMultiContent, AssistantGenMultiContent: source.AssistantGenMultiContent,
		Name: source.Name, ToolCalls: source.ToolCalls, ToolCallID: source.ToolCallID, ToolName: source.ToolName,
		ResponseMeta: source.ResponseMeta, ReasoningContent: source.ReasoningContent, Extra: source.Extra,
	}
}

// ToEino 只发送模型需要的内容，不把任务身份、发送者和原始输入记录交给模型。
func ToEino(source *Message) *schema.Message {
	if source == nil {
		return nil
	}
	return &schema.Message{
		Role: source.Role, Content: source.Content, MultiContent: source.MultiContent,
		UserInputMultiContent: source.UserInputMultiContent, AssistantGenMultiContent: source.AssistantGenMultiContent,
		Name: source.Name, ToolCalls: source.ToolCalls, ToolCallID: source.ToolCallID, ToolName: source.ToolName,
		ResponseMeta: source.ResponseMeta, ReasoningContent: source.ReasoningContent, Extra: source.Extra,
	}
}

func ToEinoMessages(messages []*Message) []*schema.Message {
	converted := make([]*schema.Message, len(messages))
	for index, message := range messages {
		converted[index] = ToEino(message)
	}
	return converted
}

// ConcatMessages 保留 Eino 对工具、推理和多模态分片的合并语义。
func ConcatMessages(chunks []*Message) (*Message, error) {
	merged, err := schema.ConcatMessages(ToEinoMessages(chunks))
	if err != nil {
		return nil, err
	}
	return FromEino(merged), nil
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
