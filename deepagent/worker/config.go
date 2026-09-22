package worker

import (
	"fmt"
	"os"
	"strings"
	"time"

	"eino-cli/deepagent/core/mcp"
	webmw "eino-cli/deepagent/core/middlewares/web"
	"eino-cli/deepagent/core/modelhub"
	"eino-cli/deepagent/manager"
	"eino-cli/deepagent/threadhost"
	"gopkg.in/yaml.v3"
)

type ManagerConfig = manager.Config

type Config struct {
	Manager                ManagerConfig     `yaml:"manager"`
	Host                   threadhost.Config `yaml:"worker"`
	Models                 []modelhub.Config `yaml:"models"`
	DefaultModel           string            `yaml:"default_model"`
	RoleModels             map[string]string `yaml:"role_models"`
	SystemPrompt           string            `yaml:"system_prompt"`
	MaxSteps               int               `yaml:"max_steps"`
	MaxModelCalls          int               `yaml:"max_model_calls"`
	ContextWindow          int64             `yaml:"context_window"`
	CompactThresholdTokens int64             `yaml:"compact_threshold_tokens"`
	KeepRecentMessages     int               `yaml:"keep_recent_messages"`
	HistoryTable           string            `yaml:"history_table"`
	MCP                    []mcp.MCPConfig   `yaml:"mcp"`
	Web                    *webmw.WebConfig  `yaml:"web"`
	SkillPaths             []string          `yaml:"skill_paths"`
	MemoryEnabled          bool              `yaml:"memory_enabled"`
	MemoryDir              string            `yaml:"memory_dir"`
	MemoryUserID           string            `yaml:"memory_user_id"`
	MemoryLeaseTTL         time.Duration     `yaml:"memory_lease_ttl"`
}

func LoadConfig(path string) (Config, error) {
	var cfg Config
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read worker config: %w", err)
	}
	var document yaml.Node
	if err = yaml.Unmarshal(data, &document); err != nil {
		return cfg, fmt.Errorf("parse worker config: %w", err)
	}
	var expand func(*yaml.Node)
	expand = func(node *yaml.Node) {
		if node.Kind == yaml.ScalarNode && node.Tag == "!!str" {
			node.Value = os.ExpandEnv(node.Value)
		}
		for _, child := range node.Content {
			expand(child)
		}
	}
	expand(&document)
	if err = document.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("parse worker config: %w", err)
	}
	return cfg, cfg.Validate()
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.Manager.MySQLDSN) == "" || strings.TrimSpace(c.Manager.RedisAddr) == "" {
		return fmt.Errorf("manager mysql_dsn and redis_addr are required")
	}
	if c.MaxSteps < 0 || c.MaxModelCalls < 0 || c.ContextWindow < 0 || c.CompactThresholdTokens < 0 || c.KeepRecentMessages < 0 {
		return fmt.Errorf("execution limits must not be negative")
	}
	if c.MemoryEnabled && strings.TrimSpace(c.MemoryDir) == "" {
		return fmt.Errorf("memory_dir is required when memory_enabled")
	}
	if c.MemoryLeaseTTL > 0 && c.MemoryLeaseTTL < 3*time.Millisecond {
		return fmt.Errorf("memory_lease_ttl must be at least 3ms")
	}
	names := make(map[string]struct{}, len(c.Models))
	for _, cfg := range c.Models {
		if strings.TrimSpace(cfg.Name) == "" {
			return fmt.Errorf("model name is required")
		}
		if _, exists := names[cfg.Name]; exists {
			return fmt.Errorf("duplicate model name %q", cfg.Name)
		}
		names[cfg.Name] = struct{}{}
	}
	if _, exists := names[c.DefaultModel]; !exists {
		return fmt.Errorf("default_model must name a configured model")
	}
	for role, name := range c.RoleModels {
		if _, exists := names[name]; !exists {
			return fmt.Errorf("role %q references unknown model %q", role, name)
		}
	}
	return nil
}
