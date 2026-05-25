package deepagent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"eino-cli/backend/agent"
	"eino-cli/backend/agent/autodream"
	"eino-cli/backend/config"
	"eino-cli/backend/consts"
	rt "eino-cli/backend/runtime"
	runtimecontext "eino-cli/backend/runtime/context"
)

type autoDreamState struct {
	lastSessionScanAt time.Time
	mu                sync.Mutex
}

func (r *Runtime) RunDream(ctx context.Context) (rt.Result, error) {
	memoryRoot := config.DreamMemoryDir()
	lastConsolidatedAt, err := autodream.ReadLastConsolidatedAt(memoryRoot)
	if err != nil {
		return rt.Result{}, fmt.Errorf("read dream lock: %w", err)
	}
	candidates, err := autodream.ListJSONLSessionCandidates(config.TranscriptDir())
	if err != nil {
		return rt.Result{}, fmt.Errorf("list dream sessions: %w", err)
	}
	sessionIDs := autodream.FilterSessionsTouchedSince(candidates, lastConsolidatedAt, "")
	if len(sessionIDs) == 0 {
		return rt.Result{Success: true, Output: "dream: no transcript sessions to consolidate"}, nil
	}
	lock, err := autodream.TryAcquireConsolidationLock(memoryRoot)
	if err != nil {
		return rt.Result{}, fmt.Errorf("acquire dream lock: %w", err)
	}
	if lock == nil {
		return rt.Result{Success: true, Output: "dream: another consolidation is already running"}, nil
	}
	prompt := autodream.BuildConsolidationPrompt(memoryRoot, config.TranscriptDir(), sessionIDs)
	result, err := runAutoDreamFork(ctx, r, []*schema.Message{schema.UserMessage(prompt)})
	if err != nil {
		autodream.RollbackConsolidationLock(lock)
		return rt.Result{}, err
	}
	appendAutoDreamCompletionMessage(r, result)
	if len(result.FilesTouched) == 0 {
		return rt.Result{Success: true, Output: "dream: completed; no memory files changed"}, nil
	}
	return rt.Result{
		Success: true,
		Output:  fmt.Sprintf("dream: improved %d memory files: %s", len(result.FilesTouched), strings.Join(result.FilesTouched, ", ")),
	}, nil
}

func runAutoDream(ctx context.Context, r *Runtime, state *autoDreamState) {

	if !shouldScanAutoDreamSessions(state) {
		return
	}

	memoryRoot := config.DreamMemoryDir()
	lastConsolidatedAt, err := autodream.ReadLastConsolidatedAt(memoryRoot)
	if err != nil {
		slog.Warn("auto-dream: read lock failed", "err", err)
		return
	}

	if !autodream.ShouldPassTimeGate(lastConsolidatedAt) {
		return
	}
	candidates, err := autodream.ListJSONLSessionCandidates(config.TranscriptDir())
	if err != nil {
		slog.Warn("auto-dream: list sessions failed", "err", err)
		return
	}
	sessionIDs := autodream.FilterSessionsTouchedSince(candidates, lastConsolidatedAt, consts.DefaultSessionID)
	if len(sessionIDs) < autodream.DefaultMinSessions {
		return
	}
	lock, err := autodream.TryAcquireConsolidationLock(memoryRoot)
	if err != nil {
		slog.Warn("auto-dream: acquire lock failed", "err", err)
		return
	}
	if lock == nil {
		return
	}
	prompt := autodream.BuildConsolidationPrompt(memoryRoot, config.TranscriptDir(), sessionIDs)
	result, err := runAutoDreamFork(ctx, r, []*schema.Message{schema.UserMessage(prompt)})
	if err != nil {
		autodream.RollbackConsolidationLock(lock)
		slog.Warn("auto-dream: fork failed", "err", err)
		return
	}
	appendAutoDreamCompletionMessage(r, result)
}

func shouldScanAutoDreamSessions(state *autoDreamState) bool {
	now := time.Now()
	state.mu.Lock()
	defer state.mu.Unlock()
	if now.Sub(state.lastSessionScanAt) < autodream.SessionScanInterval {
		return false
	}
	state.lastSessionScanAt = now
	return true
}

func runAutoDreamFork(ctx context.Context, r *Runtime, promptMessages []*schema.Message) (autodream.ForkedAgentResult, error) {
	ctx = runtimecontext.WithQuerySource(ctx, runtimecontext.QuerySourceAutoDream)
	leadAgent, err := agent.MakeAutoDreamAgent(ctx, r.cfg)
	if err != nil {
		return autodream.ForkedAgentResult{}, err
	}
	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           leadAgent,
		EnableStreaming: true,
	})
	iter := runner.Run(ctx, promptMessages)
	return collectAutoDreamEvents(iter)
}

func collectAutoDreamEvents(iter *adk.AsyncIterator[*adk.AgentEvent]) (autodream.ForkedAgentResult, error) {
	var outputs []string
	var filesTouched []string
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event == nil {
			continue
		}
		if event.Err != nil {
			return autodream.ForkedAgentResult{}, event.Err
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		msg, err := event.Output.MessageOutput.GetMessage()
		if err != nil || msg == nil {
			continue
		}
		filesTouched = append(filesTouched, getDreamTouchedFiles(msg)...)
		if msg.Role == schema.Assistant && len(msg.ToolCalls) == 0 {
			if trimmed := strings.TrimSpace(msg.Content); trimmed != "" {
				outputs = append(outputs, trimmed)
			}
		}
	}
	return autodream.ForkedAgentResult{
		FilesTouched: dedupeStrings(filesTouched),
		Output:       strings.Join(outputs, "\n"),
	}, nil
}

func appendAutoDreamCompletionMessage(r *Runtime, result autodream.ForkedAgentResult) {
	if len(result.FilesTouched) == 0 {
		return
	}
	content := fmt.Sprintf("Improved %d memory files: %s", len(result.FilesTouched), strings.Join(result.FilesTouched, ", "))
	r.mu.Lock()
	r.history = append(r.history, schema.SystemMessage(content))
	r.mu.Unlock()
}

func getDreamTouchedFiles(msg *schema.Message) []string {
	out := make([]string, 0, len(msg.ToolCalls))
	for _, call := range msg.ToolCalls {
		switch call.Function.Name {
		case "write_file", "edit_file":
			if path := parseToolFilePath(call.Function.Arguments); path != "" {
				out = append(out, path)
			}
		}
	}
	return out
}

func parseToolFilePath(args string) string {
	var payload struct {
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal([]byte(args), &payload); err != nil {
		return ""
	}
	return payload.FilePath
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
