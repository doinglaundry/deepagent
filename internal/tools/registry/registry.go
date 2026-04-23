package registry

import (
	"fmt"
	"sort"

	"eino-cli/internal/tools"
)

type Registry struct {
	tools map[string]tools.Tool
}

func New() *Registry {
	items := []tools.Tool{
		{Name: "read", Description: "Read a local file", RiskLevel: tools.RiskLevelLow},
		{Name: "ls", Description: "List a directory", RiskLevel: tools.RiskLevelLow},
		{Name: "shell", Description: "Run a shell command", RiskLevel: tools.RiskLevelHigh, RequiresApproval: true},
	}

	mapped := make(map[string]tools.Tool, len(items))
	for _, tool := range items {
		mapped[tool.Name] = tool
	}

	return &Registry{tools: mapped}
}

func (r *Registry) Get(name string) (tools.Tool, error) {
	tool, ok := r.tools[name]
	if !ok {
		return tools.Tool{}, fmt.Errorf("unknown tool: %s", name)
	}
	return tool, nil
}

func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
