package registry

import (
	"fmt"
	"sort"
	"sync"

	"eino-cli/internal/config"
	"eino-cli/internal/tools"
)

type Registry struct {
	builtin map[string]tools.Tool
	mu      sync.RWMutex
}

func New() *Registry {
	items := []tools.Tool{
		{Name: "read", Description: "Read a local file", RiskLevel: tools.RiskLevelLow, Source: "builtin", Capability: "filesystem"},
		{Name: "ls", Description: "List a directory", RiskLevel: tools.RiskLevelLow, Source: "builtin", Capability: "filesystem"},
		{Name: "shell", Description: "Run a shell command", RiskLevel: tools.RiskLevelHigh, RequiresApproval: true, Source: "builtin", Capability: "shell"},
	}

	builtins := make(map[string]tools.Tool, len(items))
	for _, tool := range items {
		builtins[tool.Name] = tool
	}

	return &Registry{builtin: builtins}
}

func (r *Registry) GetAvailableTools(_ config.Config, _ string, _ bool) ([]tools.Tool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]tools.Tool, 0, len(r.builtin))
	for _, tool := range r.builtin {
		out = append(out, tool)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (r *Registry) Get(name string) (tools.Tool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tool, ok := r.builtin[name]
	if !ok {
		return tools.Tool{}, fmt.Errorf("unknown tool: %s", name)
	}
	return tool, nil
}

func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.builtin))
	for name := range r.builtin {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
