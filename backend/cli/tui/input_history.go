package tui

import (
	"os"
	"path/filepath"
	"strings"
)

const maxInputHistory = 100

func loadInputHistory(root string) []string {
	data, err := os.ReadFile(inputHistoryPath(root))
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func saveInputHistory(root string, history []string) {
	if len(history) > maxInputHistory {
		history = history[len(history)-maxInputHistory:]
	}
	path := inputHistoryPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, []byte(strings.Join(history, "\n")+"\n"), 0o644)
}

func inputHistoryPath(root string) string {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	return filepath.Join(root, ".eino-cli", "history.txt")
}
