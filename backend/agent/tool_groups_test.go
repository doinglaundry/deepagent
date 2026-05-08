package agent

import (
	"testing"

	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/adk/prebuilt/deep"

	"eino-cli/backend/config"
)

// stubBackend / stubShell are non-nil but inert sentinels: applyToolGroups
// only checks "is the value non-nil" before installing it on deep.Config,
// so the embedded interface fields suffice without any real fs I/O.
type stubBackend struct{ filesystem.Backend }
type stubShell struct{ filesystem.Shell }

func TestApplyToolGroups(t *testing.T) {
	backend := stubBackend{}
	shell := stubShell{}

	type want struct {
		hasBackend bool
		hasShell   bool
	}

	cases := []struct {
		name    string
		profile *config.AgentConfig
		want    want
	}{
		{
			name:    "nil profile inherits all",
			profile: nil,
			want:    want{hasBackend: true, hasShell: true},
		},
		{
			name:    "nil tool_groups inherits all",
			profile: &config.AgentConfig{Name: "x", ToolGroups: nil},
			want:    want{hasBackend: true, hasShell: true},
		},
		{
			name:    "empty tool_groups disables both",
			profile: &config.AgentConfig{Name: "x", ToolGroups: []string{}},
			want:    want{hasBackend: false, hasShell: false},
		},
		{
			name:    "filesystem only",
			profile: &config.AgentConfig{Name: "x", ToolGroups: []string{"filesystem"}},
			want:    want{hasBackend: true, hasShell: false},
		},
		{
			name:    "shell only",
			profile: &config.AgentConfig{Name: "x", ToolGroups: []string{"shell"}},
			want:    want{hasBackend: false, hasShell: true},
		},
		{
			name:    "both groups, with whitespace + casing",
			profile: &config.AgentConfig{Name: "x", ToolGroups: []string{"  Filesystem ", "BASH"}},
			want:    want{hasBackend: true, hasShell: true},
		},
		{
			name:    "unknown groups silently dropped",
			profile: &config.AgentConfig{Name: "x", ToolGroups: []string{"web_search", "mcp"}},
			want:    want{hasBackend: false, hasShell: false},
		},
		{
			name:    "filesystem + unknown group keeps filesystem",
			profile: &config.AgentConfig{Name: "x", ToolGroups: []string{"filesystem", "web_search"}},
			want:    want{hasBackend: true, hasShell: false},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := &deep.Config{}
			applyToolGroups(cfg, c.profile, backend, shell)
			if (cfg.Backend != nil) != c.want.hasBackend {
				t.Errorf("Backend=%v, want hasBackend=%v", cfg.Backend != nil, c.want.hasBackend)
			}
			if (cfg.Shell != nil) != c.want.hasShell {
				t.Errorf("Shell=%v, want hasShell=%v", cfg.Shell != nil, c.want.hasShell)
			}
		})
	}
}
