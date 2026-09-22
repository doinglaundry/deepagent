package memory

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/cloudwego/eino/schema"
)

const dreamMemoryMaxBytes = 16 * 1024

func InjectDreamMemory(root string, msgs []*schema.Message) []*schema.Message {
	block := GetDreamMemoryPromptBlock(root, dreamMemoryMaxBytes)
	if block == "" {
		return msgs
	}
	out := make([]*schema.Message, 0, len(msgs)+1)
	out = append(out, msgs...)
	out = append(out, schema.SystemMessage(block))
	return out
}

func GetDreamMemoryPromptBlock(root string, maxBytes int) string {
	path := filepath.Join(root, "MEMORY.md")
	payload, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	body := strings.TrimSpace(string(payload))
	if body == "" {
		return ""
	}
	body = truncateBytes(body, maxBytes)
	return "<dream_memory>\n" + body + "\n</dream_memory>"
}

func truncateBytes(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes] + "\n..."
}
