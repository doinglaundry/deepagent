package agent

import (
	"context"
	"testing"

	"eino-cli/backend/config"
)

// dummyConfig is a minimal config.Config used by subagent build tests
// — empty Models and Agents maps, so any real model/agent resolution
// would fail, which is exactly what we want to keep these tests from
// accidentally exercising the deep.New path.
func dummyConfig() config.Config { return config.Config{} }

// TestGeneralSubagentEnabled covers the "open the general-purpose
// subagent target" gate that replaced the SubagentsConfig knob —
// enabled iff rt.SubagentEnabled is on AND we're not already inside
// a recursive subagent build.
func TestGeneralSubagentEnabled(t *testing.T) {
	rtOff := RuntimeContext{SubagentEnabled: false}
	rtOn := RuntimeContext{SubagentEnabled: true}

	if generalSubagentEnabled(context.Background(), rtOff) {
		t.Errorf("expected disabled when rt.SubagentEnabled=false")
	}
	if !generalSubagentEnabled(context.Background(), rtOn) {
		t.Errorf("expected enabled when rt.SubagentEnabled=true at depth 0")
	}
	nested := withSubagentBuild(context.Background())
	if generalSubagentEnabled(nested, rtOn) {
		t.Errorf("expected disabled inside a recursive subagent build")
	}
}

// TestSubagentBuildContextGuard verifies the depth-1 recursion cap.
// The bare context returns false (no flag); calling withSubagentBuild
// flips it; nested calls keep returning true (idempotent).
func TestSubagentBuildContextGuard(t *testing.T) {
	ctx := context.Background()
	if isSubagentBuild(ctx) {
		t.Fatal("bare context should not be flagged")
	}
	flagged := withSubagentBuild(ctx)
	if !isSubagentBuild(flagged) {
		t.Fatal("withSubagentBuild context should be flagged")
	}
	if !isSubagentBuild(withSubagentBuild(flagged)) {
		t.Fatal("nested withSubagentBuild should remain flagged")
	}
	// The original context must be unchanged (we don't mutate the
	// parent).
	if isSubagentBuild(ctx) {
		t.Fatal("parent ctx must not have been mutated")
	}
}

// TestBuildNamedSubagents_EmptyInput exercises the cheap fast-path so
// the recursive build is skipped entirely when there's nothing to
// build (matches the bypass in MakeLeadAgent above the deep.Config).
func TestBuildNamedSubagents_EmptyInput(t *testing.T) {
	got, err := buildNamedSubagents(context.Background(), RuntimeContext{}, dummyConfig(), AgentDeps{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil agents slice, got %v", got)
	}
	got, err = buildNamedSubagents(context.Background(), RuntimeContext{}, dummyConfig(), AgentDeps{}, []string{"  ", ""})
	if err != nil {
		t.Fatalf("unexpected error on whitespace-only names: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected zero agents from whitespace-only names, got %d", len(got))
	}
}
