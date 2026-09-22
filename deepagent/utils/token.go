package utils

import "github.com/cloudwego/eino/schema"

func SimpleTokenCounter(messages []*schema.Message) int {
	n := 0
	for _, m := range messages {
		if m != nil {
			n += len([]rune(m.Content))/4 + 1
		}
	}
	return n
}
