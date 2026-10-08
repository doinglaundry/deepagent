package appconfig

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"eino-cli/deepagent/config"
	"eino-cli/deepagent/graph/mcp"
	"eino-cli/deepagent/graph/modelhub"
	"eino-cli/deepagent/graph/tools"
	"eino-cli/deepagent/manager"
	"eino-cli/deepagent/threadhost"

	yaml "gopkg.in/yaml.v3"
)

type ManagerConfig = manager.Config

type Config struct {
	ComputerEnabled        bool                 `yaml:"computer_enabled"`
	BrowserOrigins         []string             `yaml:"browser_origins"`
	ComputerApps           []string             `yaml:"computer_apps"`
	FilesystemKind         string               `yaml:"filesystem_kind"`
	Docker                 config.SandboxConfig `yaml:"docker"`
	Manager                ManagerConfig        `yaml:"manager"`
	Host                   threadhost.Config    `yaml:"worker"`
	Models                 []modelhub.Config    `yaml:"models"`
	DefaultModel           string               `yaml:"default_model"`
	SystemPrompt           string               `yaml:"system_prompt"`
	MaxSteps               int                  `yaml:"max_steps"`
	MaxModelCalls          int                  `yaml:"max_model_calls"`
	ContextWindow          int64                `yaml:"context_window"`
	CompactThresholdTokens int64                `yaml:"compact_threshold_tokens"`
	KeepRecentMessages     int                  `yaml:"keep_recent_messages"`
	HistoryTable           string               `yaml:"history_table"`
	MCP                    []mcp.MCPConfig      `yaml:"mcp"`
	Web                    *tools.WebConfig     `yaml:"web"`
	SkillPaths             []string             `yaml:"skill_paths"`
	MemoryEnabled          bool                 `yaml:"memory_enabled"`
	MemoryDir              string               `yaml:"memory_dir"`
	MemoryUserID           string               `yaml:"memory_user_id"`
	MemoryLeaseTTL         time.Duration        `yaml:"memory_lease_ttl"`
}

// Load reads the shared file; Worker validates execution settings before starting.
func Load(path string) (Config, error) {
	var cfg Config
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read application config: %w", err)
	}
	// Expand scalar values after parsing so credentials cannot change YAML structure.
	var yamlRoot yaml.Node
	err = yaml.Unmarshal(data, &yamlRoot)
	if err != nil {
		return cfg, fmt.Errorf("parse application config: %w", err)
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
	expand(&yamlRoot)
	err = yamlRoot.Decode(&cfg)
	if err != nil {
		return cfg, fmt.Errorf("parse application config: %w", err)
	}
	return cfg, cfg.Manager.Validate()
}

func (c Config) Validate() error {
	if c.ComputerEnabled {
		if runtime.GOOS != "darwin" {
			return fmt.Errorf("computer use requires macOS")
		}
		err := threadhost.ValidateComputerTargets(c.BrowserOrigins, c.ComputerApps)
		if err != nil {
			return err
		}
	}
	switch c.FilesystemKind {
	case "", "local":
	case "docker":
		if strings.TrimSpace(c.Docker.Image) == "" {
			return fmt.Errorf("docker.image required for Docker workspace")
		}
	default:
		return fmt.Errorf("filesystem_kind must be local or docker")
	}
	err := c.Manager.Validate()
	if err != nil {
		return err
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
		_, exists := names[cfg.Name]
		if exists {
			return fmt.Errorf("duplicate model name %q", cfg.Name)
		}
		names[cfg.Name] = struct{}{}
	}
	_, exists := names[c.DefaultModel]
	if !exists {
		return fmt.Errorf("default_model must name a configured model")
	}
	return nil
}
