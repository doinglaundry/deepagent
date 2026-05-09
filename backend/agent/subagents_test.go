package agent

import (
	"context"
	"testing"

	"eino-cli/backend/config"
)

func dummyConfig() config.Config { return config.Config{} }

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
	if isSubagentBuild(ctx) {
		t.Fatal("parent ctx must not have been mutated")
	}
}

func TestBuildNamedSubagents_EmptyInput(t *testing.T) {
	got, err := buildNamedSubagents(context.Background(), RuntimeContext{}, dummyConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil agents slice, got %v", got)
	}
	got, err = buildNamedSubagents(context.Background(), RuntimeContext{}, dummyConfig(), []string{"  ", ""})
	if err != nil {
		t.Fatalf("unexpected error on whitespace-only names: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected zero agents from whitespace-only names, got %d", len(got))
	}
}
