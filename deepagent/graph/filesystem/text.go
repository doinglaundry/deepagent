package filesystem

import (
	"fmt"
	"strings"

	agentmodel "eino-cli/deepagent/model"
)

// ReadFileLines applies the filesystem backend line window to file content.
func ReadFileLines(content string, offset, limit *int) (window string) {
	// 处理 nil 参数：nil 表示使用默认值
	actualOffset := 0
	actualLimit := agentmodel.DefaultReadLimit
	if offset != nil {
		actualOffset = *offset
	}
	if actualOffset < 0 {
		actualOffset = 0
	}
	if limit != nil {
		actualLimit = *limit
	}
	if actualLimit <= 0 {
		actualLimit = agentmodel.DefaultReadLimit
	}
	lines := strings.SplitAfter(content, "\n")
	if len(lines) == 1 && lines[0] == "" {
		return ""
	}
	if actualOffset >= len(lines) {
		return ""
	}
	remaining := len(lines) - actualOffset
	if actualLimit > remaining {
		actualLimit = remaining
	}
	end := actualOffset + actualLimit
	return strings.Join(lines[actualOffset:end], "")
}

// ReplaceFileText applies the filesystem backend exact replacement rules.
func ReplaceFileText(content, oldString, newString string, replaceAll bool) (updated string, occurrences int, err error) {
	occurrences = strings.Count(content, oldString)
	if occurrences == 0 {
		return "", 0, fmt.Errorf("未找到要替换的字符串")
	}
	if occurrences > 1 && !replaceAll {
		return "", occurrences, fmt.Errorf("找到 %d 个匹配，请提供更精确的字符串或使用 replaceAll=true", occurrences)
	}
	if replaceAll {
		return strings.ReplaceAll(content, oldString, newString), occurrences, nil
	}
	return strings.Replace(content, oldString, newString, 1), 1, nil
}
