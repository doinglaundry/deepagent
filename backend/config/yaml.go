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

// yamlFileConfig is the wire shape we unmarshal config.yaml into.
// Field set is deliberately a strict subset of yaml/config.yaml's
// top-level sections — only the keys that actually appear in the
// file are declared here. Sections we don't (yet) consume
// (config_version, log_level, token_usage, tool_groups, tools,
// uploads, sandbox, title, summarization, memory, agents_api,
// skill_evolution, checkpointer) are intentionally omitted; add a
// field here once a code path needs to read them.
//
// Default-agent / agent / ACP configuration does NOT live in YAML
// — it's resolved entirely from environment variables and built-in
// defaults inside config.normalizeConfig.
//
// Models keeps its own private mirror because it carries legacy
// aliases (api_base, api_key=$ENV, timeout float→int) that need
// post-unmarshal normalisation in this loader.
type yamlFileConfig struct {
	DefaultModel string                  `yaml:"default_model"`
	Models       []yamlModelEntry        `yaml:"models"`
	ToolSearch   schema.ToolSearchConfig `yaml:"tool_search"`
	Skills       schema.SkillsConfig     `yaml:"skills"`
}

// yamlExtras bundles the non-model sections so the caller can fold them into
// schema.Config alongside the models map.
type yamlExtras struct {
	Skills     schema.SkillsConfig
	ToolSearch schema.ToolSearchConfig
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

	// fc.Skills and fc.ToolSearch are already the right schema
	// types — yaml.Unmarshal populated them in place. We only own
	// per-field normalisations that the YAML decoder can't
	// express: dropping deferred entries with blank names.
	extras := yamlExtras{
		Skills:     fc.Skills,
		ToolSearch: fc.ToolSearch,
	}
	if len(extras.ToolSearch.Deferred) > 0 {
		filtered := extras.ToolSearch.Deferred[:0]
		for _, d := range extras.ToolSearch.Deferred {
			if strings.TrimSpace(d.Name) == "" {
				continue
			}
			filtered = append(filtered, d)
		}
		extras.ToolSearch.Deferred = filtered
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
		// API-key resolution precedence:
		//   1. explicit api_key_env: FOO  -> read $FOO at runtime
		//   2. api_key: $FOO              -> read $FOO at runtime
		//   3. api_key: <literal-value>   -> use the literal directly
		// Literal keys are convenient for local testing; prefer
		// the env-var forms for shared / source-controlled configs.
		switch {
		case m.APIKeyEnv != "":
			mc.APIKeyEnv = m.APIKeyEnv
		case strings.HasPrefix(m.APIKey, "$"):
			mc.APIKeyEnv = strings.TrimPrefix(m.APIKey, "$")
		case strings.TrimSpace(m.APIKey) != "":
			mc.APIKey = strings.TrimSpace(m.APIKey)
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
