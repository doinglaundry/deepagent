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

	if generalSubagentEnabled(rtOff) {
		t.Errorf("expected disabled when rt.SubagentEnabled=false")
	}
	if !generalSubagentEnabled(rtOn) {
		t.Errorf("expected enabled when rt.SubagentEnabled=true")
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
