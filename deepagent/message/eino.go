package message

import "github.com/cloudwego/eino/schema"

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
