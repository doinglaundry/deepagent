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

// The block below mirrors every top-level section of
// yaml/config.yaml. Most types are private to this package —
// they're shapes we want to be able to parse cleanly even when
// the loader doesn't yet wire the values through to schema.Config.
// Once a section gains a real consumer, lift its type into the
// schema package and route it through Load() / yamlExtras.

type yamlTokenUsage struct {
	Enabled bool `yaml:"enabled"`
}

type yamlToolGroup struct {
	Name string `yaml:"name"`
}

// yamlTool captures the heterogeneous `tools:` entries. Three
// fields are universal (name/group/use); everything else (max_
// results, timeout, api_key, search_type, ...) varies per tool
// so we collect it into Extra via yaml's inline map convention.
type yamlTool struct {
	Name  string         `yaml:"name"`
	Group string         `yaml:"group"`
	Use   string         `yaml:"use"`
	Extra map[string]any `yaml:",inline"`
}

type yamlUploads struct {
	AutoConvertDocuments bool   `yaml:"auto_convert_documents"`
	PDFConverter         string `yaml:"pdf_converter"`
}

type yamlSandboxMount struct {
	HostPath      string `yaml:"host_path"`
	ContainerPath string `yaml:"container_path"`
	ReadOnly      bool   `yaml:"read_only"`
}

type yamlSandbox struct {
	Use           string             `yaml:"use"`
	AllowHostBash bool               `yaml:"allow_host_bash"`
	Mounts        []yamlSandboxMount `yaml:"mounts"`
}

type yamlTitle struct {
	Enabled   bool   `yaml:"enabled"`
	MaxWords  int    `yaml:"max_words"`
	MaxChars  int    `yaml:"max_chars"`
	ModelName string `yaml:"model_name"`
}

// yamlSummarizationThreshold backs both `trigger:` (a list) and
// `keep:` (a single record). Value is float64 because legitimate
// configs mix integer counts (tokens/messages) with fractional
// ratios (`type: fraction, value: 0.8`).
type yamlSummarizationThreshold struct {
	Type  string  `yaml:"type"`
	Value float64 `yaml:"value"`
}

type yamlSummarization struct {
	Enabled                           bool                         `yaml:"enabled"`
	ModelName                         string                       `yaml:"model_name"`
	Trigger                           []yamlSummarizationThreshold `yaml:"trigger"`
	Keep                              yamlSummarizationThreshold   `yaml:"keep"`
	TrimTokensToSummarize             int                          `yaml:"trim_tokens_to_summarize"`
	SummaryPrompt                     string                       `yaml:"summary_prompt"`
	PreserveRecentSkillCount          int                          `yaml:"preserve_recent_skill_count"`
	PreserveRecentSkillTokens         int                          `yaml:"preserve_recent_skill_tokens"`
	PreserveRecentSkillTokensPerSkill int                          `yaml:"preserve_recent_skill_tokens_per_skill"`
	SkillFileReadToolNames            []string                     `yaml:"skill_file_read_tool_names"`
}

type yamlMemory struct {
	Enabled                 bool    `yaml:"enabled"`
	StoragePath             string  `yaml:"storage_path"`
	DebounceSeconds         int     `yaml:"debounce_seconds"`
	ModelName               string  `yaml:"model_name"`
	MaxFacts                int     `yaml:"max_facts"`
	FactConfidenceThreshold float64 `yaml:"fact_confidence_threshold"`
	InjectionEnabled        bool    `yaml:"injection_enabled"`
	MaxInjectionTokens      int     `yaml:"max_injection_tokens"`
}

type yamlAgentsAPI struct {
	Enabled bool `yaml:"enabled"`
}

type yamlSkillEvolution struct {
	Enabled             bool   `yaml:"enabled"`
	ModerationModelName string `yaml:"moderation_model_name"`
}

type yamlCheckpointer struct {
	Type             string `yaml:"type"`
	ConnectionString string `yaml:"connection_string"`
}

// yamlFileConfig is the wire shape we unmarshal config.yaml into.
// Field set mirrors yaml/config.yaml's top-level sections one-for-
// one, in the order they appear in the file. Sections that don't
// yet have a downstream consumer are still parsed so that:
//   - the struct documents the file's full shape;
//   - typo'd or unknown top-level keys won't be silently swallowed
//     — yaml.v3 still won't error on them by default, but at
//     least the canonical names live here as a reference;
//   - adding a consumer later is just "expose this field via
//     yamlExtras / schema.Config", no schema spelunking required.
//
// Default-agent / agent / ACP configuration intentionally has no
// field here — those are resolved entirely from env vars + built-
// in defaults inside config.normalizeConfig, and the YAML doesn't
// declare them.
//
// Models / yamlTool keep their own private mirrors because they
// carry legacy aliases or heterogeneous extras that the YAML
// decoder can't express directly.
type yamlFileConfig struct {
	DefaultModel   string                  `yaml:"default_model"`
	ConfigVersion  int                     `yaml:"config_version"`
	LogLevel       string                  `yaml:"log_level"`
	TokenUsage     yamlTokenUsage          `yaml:"token_usage"`
	Models         []yamlModelEntry        `yaml:"models"`
	ToolGroups     []yamlToolGroup         `yaml:"tool_groups"`
	Tools          []yamlTool              `yaml:"tools"`
	ToolSearch     schema.ToolSearchConfig `yaml:"tool_search"`
	Uploads        yamlUploads             `yaml:"uploads"`
	Sandbox        yamlSandbox             `yaml:"sandbox"`
	Skills         schema.SkillsConfig     `yaml:"skills"`
	Title          yamlTitle               `yaml:"title"`
	Summarization  yamlSummarization       `yaml:"summarization"`
	Memory         yamlMemory              `yaml:"memory"`
	AgentsAPI      yamlAgentsAPI           `yaml:"agents_api"`
	SkillEvolution yamlSkillEvolution      `yaml:"skill_evolution"`
	Checkpointer   yamlCheckpointer        `yaml:"checkpointer"`
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
