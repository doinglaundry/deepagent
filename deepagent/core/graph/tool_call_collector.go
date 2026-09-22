package graph

import (
	"context"
	"eino-cli/deepagent/core/middlewares/repairjson"
	"encoding/json"
	"fmt"
	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/schema"
	"log/slog"
	"strings"
)

type ToolCallCollector struct{ partialCalls map[string]*schema.ToolCall }

func NewToolCallCollector() *ToolCallCollector {
	return &ToolCallCollector{partialCalls: make(map[string]*schema.ToolCall)}
}

// Collect 根据 chunk 的内容 merge tool call，并返回这次已经收集完毕的工具调用
func (t *ToolCallCollector) Collect(ctx context.Context, chunk *schema.Message) []schema.ToolCall {
	var completeCalls []schema.ToolCall
	for _, delta := range chunk.ToolCalls {
		if delta.Index == nil && delta.ID == "" {
			slog.WarnContext(ctx, fmt.Sprintf("[ToolCallCollector::Collect] tool call index and id are both empty. function:%+v", delta.Function))
			continue
		}
		key, partialCall := t.mergePartialCall(delta)
		if partialCall.Function.Name == "" || !IsToolArgumentsComplete(partialCall.Function.Arguments) {
			continue
		}
		completedCall := *partialCall
		completeCalls = append(completeCalls, completedCall)
		delete(t.partialCalls, key)
	}
	return completeCalls
}

func (t *ToolCallCollector) mergePartialCall(delta schema.ToolCall) (string, *schema.ToolCall) {
	key, previousKey := getPartialToolKeys(delta)
	partialCallFromKey := t.partialCalls[key]
	var partialCallFromPreviousKey *schema.ToolCall
	if previousKey != "" {
		partialCallFromPreviousKey = t.partialCalls[previousKey]
	}
	if partialCallFromKey == nil && partialCallFromPreviousKey == nil {
		partialCall := &delta
		t.partialCalls[key] = partialCall
		return key, partialCall
	}
	if partialCallFromKey != nil {
		mergeToolCallDelta(partialCallFromKey, delta)
		return key, partialCallFromKey
	}
	t.partialCalls[key] = partialCallFromPreviousKey
	delete(t.partialCalls, previousKey)
	mergeToolCallDelta(partialCallFromPreviousKey, delta)
	return key, partialCallFromPreviousKey
}

func mergeToolCallDelta(partialCall *schema.ToolCall, delta schema.ToolCall) {
	if partialCall.ID == "" && delta.ID != "" {
		partialCall.ID = delta.ID
	}
	if partialCall.Function.Name == "" && delta.Function.Name != "" {
		partialCall.Function.Name = delta.Function.Name
	}
	if delta.Index != nil {
		partialCall.Index = delta.Index
	}
	if delta.Type != "" {
		partialCall.Type = delta.Type
	}
	if delta.Function.Arguments != "" {
		partialCall.Function.Arguments += delta.Function.Arguments
	}
}

func getPartialToolKeys(delta schema.ToolCall) (string, string) {
	if delta.Index == nil && delta.ID == "" {
		return "", ""
	}
	if delta.Index == nil && delta.ID != "" {
		return "id_" + delta.ID, ""
	}
	if delta.Index != nil && delta.ID == "" {
		return fmt.Sprintf("index_%d", *delta.Index), ""
	}
	return fmt.Sprintf("index_%d", *delta.Index), "id_" + delta.ID
}

func (t *ToolCallCollector) GetPartialToolCalls() []schema.ToolCall {
	partialCalls := make([]schema.ToolCall, 0, len(t.partialCalls))
	for _, tc := range t.partialCalls {
		partialCalls = append(partialCalls, *tc)
	}
	return partialCalls
}

// GetRepairedToolCalls 应该在流 eof 后调用, 用于获取修复后的工具调用
func (t *ToolCallCollector) GetRepairedToolCalls(ctx context.Context) []schema.ToolCall {
	repairedCalls := make([]schema.ToolCall, 0, len(t.partialCalls))
	for _, tc := range t.partialCalls {
		if tc.Function.Name != "" && tc.Function.Arguments != "" {
			repaired, repairErr := repairjson.RepairJSON(tc.Function.Arguments)
			if repairErr != nil || !json.Valid([]byte(repaired)) {
				slog.WarnContext(ctx, fmt.Sprintf("[ToolCallCollector::GetRepairedToolCalls] invalid JSON. function:%+v,arg:%s", tc.Function, tc.Function.Arguments))
				continue
			}
			if !IsToolArgumentsComplete(repaired) {
				slog.WarnContext(ctx, fmt.Sprintf("[ToolCallCollector::GetRepairedToolCalls] repaired arguments is not complete. function:%+v,arg:%s", tc.Function, repaired))
				continue
			}
			newTc := *tc
			newTc.Function.Arguments = repaired
			repairedCalls = append(repairedCalls, newTc)
		}
	}
	return repairedCalls
}

// IsToolArgumentsComplete 检查参数完整性 - 简化版本
func IsToolArgumentsComplete(args string) bool {
	args = strings.TrimSpace(args)
	if len(args) == 0 {
		return false
	}
	if args == "{}" {
		return true
	}
	if !strings.HasPrefix(args, "{") || !strings.HasSuffix(args, "}") {
		return false
	}
	// 简单的括号平衡检查
	bracketCount := 0
	inString := false
	escaped := false
	for i, char := range args {
		if escaped {
			escaped = false
			continue
		}
		switch char {
		case '\\':
			escaped = true
		case '"':
			inString = !inString
		case '{':
			if !inString {
				bracketCount++
			}
		case '}':
			if !inString {
				bracketCount--
			}
		}
		if bracketCount == 0 && i < len(args)-1 {
			return false
		}
	}
	if bracketCount != 0 {
		return false
	}
	// JSON 验证
	var test map[string]interface{}
	return sonic.Unmarshal([]byte(args), &test) == nil
}
