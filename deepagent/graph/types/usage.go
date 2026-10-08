package types

// ContextTokenUsage 记录上下文占用估算及最近一次模型调用的输入、输出用量。
// JSON 字段名保留已有 checkpoint 格式，避免改名后续跑丢失用量。
type ContextTokenUsage struct {
	MaxContextTokens int64 `json:"ContextWindow"`             // 上下文容量上限
	TotalTokens      int64 `json:"CurrentTotal"`              // 当前上下文占用估算
	PromptTokens     int64 `json:"LastModelPromptTokens"`     // 最近一次模型的输入用量
	CompletionTokens int64 `json:"LastModelCompletionTokens"` // 最近一次模型的输出用量
}
