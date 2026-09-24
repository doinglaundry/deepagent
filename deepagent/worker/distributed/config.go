// Package distributed assembles production Worker dependencies. It never falls back
// to a local runtime when shared storage or model configuration is unavailable.
package distributed

import (
	"eino-cli/deepagent/config"
	"eino-cli/deepagent/core/mcp"
	"eino-cli/deepagent/core/modelhub"
	"eino-cli/deepagent/manager/compat"
	"eino-cli/deepagent/worker/managed"
	"fmt"
	"gopkg.in/yaml.v3"
	"net/http"
	"os"
	"strings"
	"time"
)

// Preserve the Worker config spelling while Core owns the MCP protocol.
type MCPConfig = mcp.MCPConfig

// SearchURL accepts a GET search endpoint with a q parameter or {query} placeholder.
// Headers apply only to the configured search endpoint, never arbitrary read URLs.
type WebConfig struct {
	Enabled        bool              `yaml:"enabled"`
	SearchURL      string            `yaml:"search_url"`
	Headers        map[string]string `yaml:"headers"`
	TimeoutSeconds int               `yaml:"timeout_seconds"`
	MaxBytes       int64             `yaml:"max_bytes"`
	HTTPClient     *http.Client      `yaml:"-"`
}

type Config struct {
	WorkspaceKind          string               `yaml:"workspace_kind"`
	Docker                 config.SandboxConfig `yaml:"docker"`
	CompactThresholdTokens int                  `yaml:"compact_threshold_tokens"`
	KeepRecentMessages     int                  `yaml:"keep_recent_messages"`
	Checkpoint             CheckpointConfig     `yaml:"checkpoint"`
	MemoryScanInterval     time.Duration        `yaml:"memory_scan_interval"`
	MemoryLeaseTTL         time.Duration        `yaml:"memory_lease_ttl"`
	Web                    WebConfig            `yaml:"web"`
	MemoryUserID           string               `yaml:"memory_user_id"`
	Manager                manager.Config       `yaml:"-"`
	Worker                 managed.Config       `yaml:"worker"`
	Models                 []modelhub.Config    `yaml:"models"`
	DefaultModel           string               `yaml:"default_model"`
	RoleModels             map[string]string    `yaml:"role_models"`
	SystemPrompt           string               `yaml:"system_prompt"`
	MaxSteps               int                  `yaml:"max_steps"`
	MaxModelCalls          int                  `yaml:"max_model_calls"`
	MCP                    []MCPConfig          `yaml:"mcp"`
	SkillPaths             []string             `yaml:"skill_paths"`
	MemoryEnabled          bool                 `yaml:"memory_enabled"`
	MemoryDir              string               `yaml:"memory_dir"`
}

func LoadConfig(path string) (Config, error) {
	var c Config
	data, err := os.ReadFile(path)
	if err != nil {
		return c, fmt.Errorf("read worker config: %w", err)
	}
	var wire struct {
		Config  `yaml:",inline"`
		Manager struct {
			Namespace     string `yaml:"namespace"`
			MySQLDSN      string `yaml:"mysql_dsn"`
			RedisAddr     string `yaml:"redis_addr"`
			RedisPassword string `yaml:"redis_password"`
			RedisDB       int    `yaml:"redis_db"`
		} `yaml:"manager"`
	}
	var document yaml.Node
	if err = yaml.Unmarshal(data, &document); err != nil {
		return c, fmt.Errorf("parse worker config: %w", err)
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
	if err = document.Decode(&wire); err != nil {
		return c, fmt.Errorf("parse worker config: %w", err)
	}
	c = wire.Config
	c.Manager = manager.Config{Namespace: wire.Manager.Namespace, MySQLDSN: wire.Manager.MySQLDSN, RedisAddr: wire.Manager.RedisAddr, RedisPassword: wire.Manager.RedisPassword, RedisDB: wire.Manager.RedisDB}
	if c.Manager.Namespace == "" {
		c.Manager.Namespace = "default"
	}
	if c.Manager.MySQLDSN == "" {
		c.Manager.MySQLDSN = os.Getenv("DEEPAGENT_MYSQL_DSN")
	}
	if c.Manager.RedisAddr == "" {
		c.Manager.RedisAddr = os.Getenv("DEEPAGENT_REDIS_ADDR")
	}
	if c.Manager.RedisAddr == "" {
		c.Manager.RedisAddr = "localhost:6379"
	}
	return c, c.Validate()
}
func (c Config) Validate() error {
	switch c.WorkspaceKind {
	case "", "local":
	case "docker":
		if strings.TrimSpace(c.Docker.Image) == "" {
			return fmt.Errorf("docker.image required for Docker workspace")
		}
	default:
		return fmt.Errorf("workspace_kind must be local or docker")
	}
	switch c.Checkpoint.Backend {
	case "", "mysql", "redis":
	case "file":
		if c.Checkpoint.Path == "" {
			return fmt.Errorf("checkpoint.path required for file storage")
		}
	default:
		return fmt.Errorf("checkpoint.backend must be mysql, redis, or file")
	}
	if strings.TrimSpace(c.Manager.Namespace) == "" || strings.TrimSpace(c.Manager.MySQLDSN) == "" || strings.TrimSpace(c.Manager.RedisAddr) == "" {
		return fmt.Errorf("manager namespace, mysql_dsn and redis_addr required")
	}
	if c.MemoryLeaseTTL > 0 && c.MemoryLeaseTTL < 3*time.Millisecond {
		return fmt.Errorf("memory_lease_ttl must be at least 3ms")
	}
	if c.MemoryScanInterval < 0 || c.MemoryLeaseTTL < 0 {
		return fmt.Errorf("memory intervals must not be negative")
	}
	if c.KeepRecentMessages < 0 {
		return fmt.Errorf("keep_recent_messages must not be negative")
	}
	if c.MaxSteps < 0 || c.MaxModelCalls < 0 {
		return fmt.Errorf("execution budgets must not be negative")
	}
	names := map[string]bool{}
	for _, m := range c.Models {
		if m.Name == "" || names[m.Name] {
			return fmt.Errorf("model names must be nonempty and unique")
		}
		names[m.Name] = true
	}
	if !names[c.DefaultModel] {
		return fmt.Errorf("default_model must name a configured Worker model")
	}
	for role, name := range c.RoleModels {
		if !names[name] {
			return fmt.Errorf("role %q references unknown model %q", role, name)
		}
	}
	if c.MemoryEnabled && c.MemoryDir == "" {
		return fmt.Errorf("memory_dir required when memory_enabled")
	}
	return nil
}
