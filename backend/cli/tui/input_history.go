package tui

import (
	"os"
	"strings"

	"eino-cli/backend/config"
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
	if err := os.MkdirAll(config.BaseDir(&config.Config{RootDir: normalizeHistoryRoot(root)}), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, []byte(strings.Join(history, "\n")+"\n"), 0o644)
}

func inputHistoryPath(root string) string {
	return config.InputHistoryPath(&config.Config{RootDir: normalizeHistoryRoot(root)})
}

func normalizeHistoryRoot(root string) string {
	if strings.TrimSpace(root) == "" {
		return "."
	}
	return root
}
