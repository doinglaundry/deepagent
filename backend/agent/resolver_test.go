package agent

import (
	"testing"

	"eino-cli/backend/config"
)

func TestValidateAgentName(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "", false},
		{"   ", "", false},
		{"default", "default", false},
		{"my-agent_42", "my-agent_42", false},
		{"bad name", "", true},
		{"bad/name", "", true},
		{"中文", "", true},
	}
	for _, tc := range cases {
		got, err := ValidateAgentName(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("ValidateAgentName(%q): expected error, got %q", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ValidateAgentName(%q): unexpected error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("ValidateAgentName(%q): got %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestGetModelConfig(t *testing.T) {
	cfg := config.Config{
		Models: map[string]*config.ModelConfig{
			"kimi": {Name: "kimi", Provider: "kimi", Model: "moonshot-v1-8k"},
		},
	}

	modelCfg, err := GetModelConfig("kimi", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if modelCfg == nil || modelCfg.Name != "kimi" {
		t.Fatalf("got %v, want model with Name=kimi", modelCfg)
	}

	if _, err := GetModelConfig("", cfg); err == nil {
		t.Fatalf("expected error on empty model name")
	}
	if _, err := GetModelConfig("missing", cfg); err == nil {
		t.Fatalf("expected error on unknown model name")
	}
}
