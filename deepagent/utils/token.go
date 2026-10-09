package utils

import (
	agentmodel "eino-cli/deepagent/model"
)

func SimpleTokenCounter(messages []*agentmodel.Message) int {
	n := 0
	for _, m := range messages {
		if m != nil {
			n += len([]rune(m.Content))/4 + 1
		}
	}
	return n
}
