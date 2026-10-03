package types

type ContextUsageSource string

const (
	ContextUsageSourceEstimated  ContextUsageSource = "estimated"
	ContextUsageSourceModelUsage ContextUsageSource = "model_usage"
)

// ContextUsageSnapshot combines the provider baseline with later local estimates.
// Conversation owns the live values; checkpoints carry snapshots only.
type ContextUsageSnapshot struct {
	ContextWindow             int64
	LastModelTotal            int64
	EstimatedAfterLastModel   int64
	CurrentTotal              int64
	Source                    ContextUsageSource
	LastModelPromptTokens     int64
	LastModelCompletionTokens int64
}

// ContextSnapshot binds usage to the durable history it describes.
type ContextSnapshot struct {
	Usage         ContextUsageSnapshot
	HistoryCursor int64
}
