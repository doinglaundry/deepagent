package utils

import (
	messagepkg "eino-cli/deepagent/message"
)

func SimpleTokenCounter(messages []*messagepkg.Message) int {
	n := 0
	for _, m := range messages {
		if m != nil {
			n += len([]rune(m.Content))/4 + 1
		}
	}
	return n
}
