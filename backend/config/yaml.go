package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"eino-cli/backend/config/schema"

	"gopkg.in/yaml.v3"
)

const defaultYAMLModel = "kimi"

type yamlModelEntry struct {
	Name           string  `yaml:"name"`
	Provider       string  `yaml:"provider"`
	Model          string  `yaml:"model"`
	BaseURL        string  `yaml:"base_url"`
	APIBase        string  `yaml:"api_base"`
	APIKey         string  `yaml:"api_key"`
	APIKeyEnv      string  `yaml:"api_key_env"`
	Timeout        float64 `yaml:"timeout"`
	TimeoutSeconds int     `yaml:"timeout_seconds"`
}

type yamlSkillsEntry struct {
	Paths []string `yaml:"paths"`
}

type yamlDeferredToolEntry struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

type yamlToolSearchEntry struct {
	Enabled  bool                    `yaml:"enabled"`
	Deferred []yamlDeferredToolEntry `yaml:"deferred"`
}

type yamlACPAgentEntry struct {
	Description string `yaml:"description"`
}

type yamlACPEntry struct {
	Agents map[string]yamlACPAgentEntry `yaml:"agents"`
}

// yamlAgentEntry mirrors schema.AgentConfig + the deerflow agent yaml
// schema. Both inline (top-level `agents:` block) and per-directory
// (`<agents_dir>/<name>/config.yaml`) loaders share this shape.
type yamlAgentEntry struct {
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description"`
	Instruction  string   `yaml:"instruction"`
	MaxIteration int      `yaml:"max_iteration"`
	Model        string   `yaml:"model"`
	ToolGroups   []string `yaml:"tool_groups"`
	Skills       []string `yaml:"skills"`
}

type yamlFileConfig struct {
	DefaultModel string                    `yaml:"default_model"`
	DefaultAgent string                    `yaml:"default_agent"`
	AgentsDir    string                    `yaml:"agents_dir"`
	Models       []yamlModelEntry          `yaml:"models"`
	Agents       map[string]yamlAgentEntry `yaml:"agents"`
	Skills       yamlSkillsEntry           `yaml:"skills"`
	ToolSearch   yamlToolSearchEntry       `yaml:"tool_search"`
	ACP          yamlACPEntry              `yaml:"acp"`
}

// yamlExtras bundles the non-model sections so the caller can fold them into
// schema.Config alongside the models map.
type yamlExtras struct {
	DefaultAgent string
	AgentsDir    string
	Agents       map[string]schema.AgentConfig
	Skills       schema.SkillsConfig
	ToolSearch   schema.ToolSearchConfig
	ACP          schema.ACPConfig
}

func loadFromYAML(path string) (map[string]*schema.ModelConfig, yamlExtras, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, yamlExtras{}, nil
		}
		return nil, yamlExtras{}, fmt.Errorf("read yaml config: %w", err)
	}
	var fc yamlFileConfig
	if err = yaml.Unmarshal(data, &fc); err != nil {
		return nil, yamlExtras{}, fmt.Errorf("parse yaml config: %w", err)
	}

	extras := yamlExtras{
		DefaultAgent: strings.TrimSpace(fc.DefaultAgent),
		AgentsDir:    strings.TrimSpace(fc.AgentsDir),
		Skills:       schema.SkillsConfig{Paths: append([]string(nil), fc.Skills.Paths...)},
		ToolSearch: schema.ToolSearchConfig{
			Enabled: fc.ToolSearch.Enabled,
		},
		ACP: schema.ACPConfig{},
	}
	if len(fc.Agents) > 0 {
		extras.Agents = make(map[string]schema.AgentConfig, len(fc.Agents))
		for key, a := range fc.Agents {
			name := strings.TrimSpace(a.Name)
			if name == "" {
				name = key
			}
			extras.Agents[key] = schema.AgentConfig{
				Name:         name,
				Description:  a.Description,
				Instruction:  a.Instruction,
				MaxIteration: a.MaxIteration,
				Model:        strings.TrimSpace(a.Model),
				ToolGroups:   append([]string(nil), a.ToolGroups...),
				Skills:       cloneSkills(a.Skills),
			}
		}
	}
	for _, d := range fc.ToolSearch.Deferred {
		if strings.TrimSpace(d.Name) == "" {
			continue
		}
		extras.ToolSearch.Deferred = append(extras.ToolSearch.Deferred,
			schema.DeferredToolEntry{Name: d.Name, Description: d.Description})
	}
	if len(fc.ACP.Agents) > 0 {
		extras.ACP.Agents = make(map[string]schema.ACPAgentEntry, len(fc.ACP.Agents))
		for name, a := range fc.ACP.Agents {
			extras.ACP.Agents[name] = schema.ACPAgentEntry{Description: a.Description}
		}
	}

	if len(fc.Models) == 0 {
		return map[string]*schema.ModelConfig{
			defaultYAMLModel: {
				Name:           defaultYAMLModel,
				Provider:       "kimi",
				Model:          "moonshot-v1-8k",
				BaseURL:        "https://api.moonshot.cn/v1",
				APIKeyEnv:      "MOONSHOT_API_KEY",
				TimeoutSeconds: 60,
			},
		}, extras, nil
	}

	models := make(map[string]*schema.ModelConfig, len(fc.Models))
	for _, m := range fc.Models {
		mc := schema.ModelConfig{
			Name:     m.Name,
			Provider: m.Provider,
			Model:    m.Model,
		}
		if m.BaseURL != "" {
			mc.BaseURL = m.BaseURL
		} else if m.APIBase != "" {
			mc.BaseURL = m.APIBase
		}
		if m.APIKeyEnv != "" {
			mc.APIKeyEnv = m.APIKeyEnv
		} else if v, ok := strings.CutPrefix(m.APIKey, "$"); ok {
			mc.APIKeyEnv = v
		}
		if m.TimeoutSeconds > 0 {
			mc.TimeoutSeconds = m.TimeoutSeconds
		} else if m.Timeout > 0 {
			mc.TimeoutSeconds = int(m.Timeout)
		}
		if mc.Provider == "" {
			mc.Provider = inferProvider(mc.BaseURL, m.Name)
		}
		models[m.Name] = ToPtr(mc)
	}

	return models, extras, nil
}

func inferProvider(baseURL, name string) string {
	lower := strings.ToLower(baseURL + name)
	switch {
	case strings.Contains(lower, "moonshot") || strings.Contains(lower, "kimi"):
		return "kimi"
	case strings.Contains(lower, "openai"):
		return "openai"
	case strings.Contains(lower, "anthropic") || strings.Contains(lower, "claude"):
		return "claude"
	default:
		return "openai"
	}
}

// ToPtr returns a pointer to v.
func ToPtr[T any](v T) *T {
	return &v
}

// cloneSkills preserves the nil-vs-empty-slice distinction the agent
// profile semantics depend on (nil = inherit, [] = strict empty subset).
// A naive append([]string(nil), src...) would erase that distinction by
// always returning nil for empty inputs.
func cloneSkills(src []string) []string {
	if src == nil {
		return nil
	}
	out := make([]string, len(src))
	copy(out, src)
	return out
}
