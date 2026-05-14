package agent

import (
	"github.com/cloudwego/eino/schema"

	"eino-cli/backend/config"
	memorystore "eino-cli/backend/memory/store"
)

// GetMemoryPromptBlock loads the memory snapshot for agentName, renders it as
// a "<memory>...</memory>" block, and budgets the body under maxTokens.
// Returns "" for any short-circuit case (nil store, load error, empty data),
// so callers can drop it straight into a string template.
func GetMemoryPromptBlock(store *memorystore.Store, agentName string, maxTokens int) string {
	if store == nil {
		return ""
	}
	data, err := store.Load(agentName)
	if err != nil {
		return ""
	}
	body := formatMemoryForInjection(data, maxTokens)
	if body == "" {
		return ""
	}
	return "<memory>\n" + body + "\n</memory>"
}

// InjectMemory prepends the memory block as a system message. Honours
// cfg.Enabled and cfg.InjectionEnabled so the middleware can keep wiring it
// in unconditionally; falsy config / empty memory both return msgs unchanged.
func InjectMemory(
	store *memorystore.Store,
	cfg config.Memory,
	agentName string,
	msgs []*schema.Message,
) []*schema.Message {
	if !cfg.Enabled || !cfg.InjectionEnabled {
		return msgs
	}
	block := GetMemoryPromptBlock(store, agentName, cfg.MaxInjectionTokens)
	if block == "" {
		return msgs
	}
	out := make([]*schema.Message, 0, len(msgs)+1)
	out = append(out, msgs...)
	out = append(out, schema.SystemMessage(block))
	return out
}
