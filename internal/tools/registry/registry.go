package registry

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"eino-cli/internal/config"
	"eino-cli/internal/plugin/gateway"
	"eino-cli/internal/tools"
)

type Registry struct {
	builtin map[string]tools.Tool
	plugin  map[string]tools.Tool
	gateway *gateway.Gateway
	mu      sync.RWMutex
}

func New(pluginGateway *gateway.Gateway) *Registry {
	items := []tools.Tool{
		{Name: "read", Description: "Read a local file", RiskLevel: tools.RiskLevelLow, Source: "builtin", Capability: "filesystem"},
		{Name: "ls", Description: "List a directory", RiskLevel: tools.RiskLevelLow, Source: "builtin", Capability: "filesystem"},
		{Name: "shell", Description: "Run a shell command", RiskLevel: tools.RiskLevelHigh, RequiresApproval: true, Source: "builtin", Capability: "shell"},
	}

	builtins := make(map[string]tools.Tool, len(items))
	for _, tool := range items {
		builtins[tool.Name] = tool
	}

	return &Registry{builtin: builtins, plugin: map[string]tools.Tool{}, gateway: pluginGateway}
}

func (r *Registry) RefreshPluginTools(ctx context.Context) error {
	if r == nil || r.gateway == nil || !r.gateway.Enabled() {
		return nil
	}
	items, err := r.gateway.ListTools(ctx)
	if err != nil {
		return err
	}

	next := make(map[string]tools.Tool, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		if _, exists := r.builtin[name]; exists {
			continue
		}
		next[name] = item
	}

	r.mu.Lock()
	r.plugin = next
	r.mu.Unlock()
	return nil
}

func (r *Registry) GetAvailableTools(cfg config.Config, agentName string, planMode bool) ([]tools.Tool, error) {
	_ = cfg
	_ = agentName
	_ = planMode
	_ = r.RefreshPluginTools(context.Background())

	merged := r.snapshot()
	out := make([]tools.Tool, 0, len(merged))
	for _, tool := range merged {
		out = append(out, tool)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (r *Registry) Get(name string) (tools.Tool, error) {
	_ = r.RefreshPluginTools(context.Background())
	merged := r.snapshot()
	tool, ok := merged[name]
	if !ok {
		return tools.Tool{}, fmt.Errorf("unknown tool: %s", name)
	}
	return tool, nil
}

func (r *Registry) Names() []string {
	_ = r.RefreshPluginTools(context.Background())
	merged := r.snapshot()
	names := make([]string, 0, len(merged))
	for name := range merged {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (r *Registry) InvokePluginTool(ctx context.Context, name string, args []string) (tools.Result, error) {
	if r == nil || r.gateway == nil || !r.gateway.Enabled() {
		return tools.Result{}, fmt.Errorf("plugin gateway is unavailable")
	}
	return r.gateway.InvokeTool(ctx, name, args)
}

func (r *Registry) snapshot() map[string]tools.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	merged := make(map[string]tools.Tool, len(r.builtin)+len(r.plugin))
	for name, tool := range r.builtin {
		merged[name] = tool
	}
	for name, tool := range r.plugin {
		if _, exists := merged[name]; exists {
			continue
		}
		merged[name] = tool
	}
	return merged
}
