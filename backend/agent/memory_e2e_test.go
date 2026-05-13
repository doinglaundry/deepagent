package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"eino-cli/backend/agent/middlewares"
	"eino-cli/backend/config"
	memorystore "eino-cli/backend/memory/store"
)

// E2E covers the full wiring: build the middleware chain via
// GetChatModelMiddlewares, fire the Memory middleware's AfterModel hook with
// a fake chat model, and assert the rich-schema JSON lands at the expected
// per-agent path. This is what we'd otherwise verify by hand running
// eino-cli locally.
func TestMemory_E2E_AfterModelHookWritesPerAgentFile(t *testing.T) {
	root := t.TempDir()

	cfg := &config.Config{
		RootDir:      root,
		DefaultModel: "primary",
		Models: map[string]*config.ModelConfig{
			"primary": {Name: "primary", Provider: "kimi"},
		},
		Memory: config.Memory{
			Enabled:                 true,
			InjectionEnabled:        true,
			MaxInjectionTokens:      0,
			DebounceSeconds:         0,
			MaxFacts:                10,
			FactConfidenceThreshold: 0.5,
		},
	}
	resp := mustMarshal(t, updatePayload{
		User: map[string]sectionUpdate{
			"workContext": {Summary: "Go backend dev", ShouldUpdate: true},
		},
		NewFacts: []factUpdate{
			{Content: "prefers tabs", Category: "preference", Confidence: 0.95},
		},
	})
	chat := &fakeChatModel{response: resp}

	chain := GetChatModelMiddlewares(context.Background(), "alice", false, cfg, chat)

	var memMW *middlewares.Memory
	for _, mw := range chain {
		if m, ok := mw.(*middlewares.Memory); ok {
			memMW = m
			break
		}
	}
	if memMW == nil {
		t.Fatal("Memory middleware missing from chain when cfg.Memory.Enabled")
	}

	state := &adk.ChatModelAgentState{
		Messages: []*schema.Message{
			schema.UserMessage("I prefer tabs over spaces."),
			schema.AssistantMessage("Got it, sticking with tabs.", nil),
		},
	}
	_, _, err := memMW.AfterModelRewriteState(context.Background(), state, &adk.ModelContext{})
	if err != nil {
		t.Fatalf("AfterModelRewriteState: %v", err)
	}

	wantPath := filepath.Join(root, ".eino-cli", "memory", "agents", "alice.json")
	waitForFile(t, wantPath, 5*time.Second)

	raw, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read landed memory file: %v", err)
	}
	var got memorystore.MemoryData
	err = json.Unmarshal(raw, &got)
	if err != nil {
		t.Fatalf("unmarshal landed memory: %v", err)
	}

	if got.User.WorkContext.Summary != "Go backend dev" {
		t.Errorf("workContext summary not applied; got %+v", got.User.WorkContext)
	}
	if got.User.WorkContext.UpdatedAt == "" {
		t.Errorf("workContext UpdatedAt should be stamped; got %+v", got.User.WorkContext)
	}
	if len(got.Facts) != 1 || got.Facts[0].Content != "prefers tabs" {
		t.Errorf("expected one new fact, got %+v", got.Facts)
	}
	if got.Facts[0].Source != "llm" {
		t.Errorf("new fact Source should be 'llm', got %q", got.Facts[0].Source)
	}
}

// AfterModel hook never goes near the LLM nor the store when memory is off,
// so the chain stays clean for users who haven't opted in.
func TestMemory_E2E_DisabledLeavesChainAndDiskUntouched(t *testing.T) {
	root := t.TempDir()

	cfg := &config.Config{
		RootDir:      root,
		DefaultModel: "primary",
		Models: map[string]*config.ModelConfig{
			"primary": {Name: "primary", Provider: "kimi"},
		},
		Memory: config.Memory{Enabled: false},
	}
	chat := &fakeChatModel{response: "{}"}
	chain := GetChatModelMiddlewares(context.Background(), "alice", false, cfg, chat)

	for _, mw := range chain {
		if _, ok := mw.(*middlewares.Memory); ok {
			t.Fatal("Memory middleware must not be present when cfg.Memory.Enabled=false")
		}
	}
	if chat.calls != 0 {
		t.Fatalf("chat model must not be invoked during chain assembly, got calls=%d", chat.calls)
	}

	memDir := filepath.Join(root, ".eino-cli", "memory")
	_, err := os.Stat(memDir)
	if !os.IsNotExist(err) {
		t.Errorf("memory dir must not be created when memory is disabled; stat err=%v", err)
	}
}

// waitForFile polls until the path exists and is non-empty, or the deadline
// elapses. The Memory middleware launches Extract in a goroutine, so even
// after AfterModelRewriteState returns we still need to wait for the LLM
// call + parse + Save to complete.
func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		info, err := os.Stat(path)
		if err == nil && info.Size() > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("file did not appear within %s: %s", timeout, path)
}
