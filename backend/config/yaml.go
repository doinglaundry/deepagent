package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

const defaultYAMLModel = "kimi"

// ModelEntry is the wire shape for a single `models:` list item.
// It carries legacy aliases (api_base, timeout) that the runtime
// ModelConfig doesn't speak — loadFromYAML normalises those into
// the canonical fields.
type ModelEntry struct {
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
// yaml/config.yaml. Types are exported and live in this package
// so the YAML structure has a single authoritative declaration —
// no more `schema.*` vs `yaml*` parallel definitions. Sections
// without a runtime consumer are still parsed so that:
//   - the struct documents the file's full shape;
//   - typo'd or unknown top-level keys are easy to track down
//     against the canonical names declared here.
// Once a section gains a real consumer, expose it through Load()
// or the loader's return value.

type TokenUsage struct {
	Enabled bool `yaml:"enabled"`
}

type ToolGroup struct {
	Name string `yaml:"name"`
}

// Tool captures the heterogeneous `tools:` entries. Three fields
// are universal (name/group/use); everything else (max_results,
// timeout, api_key, search_type, ...) varies per tool so we
// collect it into Extra via yaml's inline map convention.
type Tool struct {
	Name  string         `yaml:"name"`
	Group string         `yaml:"group"`
	Use   string         `yaml:"use"`
	Extra map[string]any `yaml:",inline"`
}

type Uploads struct {
	AutoConvertDocuments bool   `yaml:"auto_convert_documents"`
	PDFConverter         string `yaml:"pdf_converter"`
}

type SandboxMount struct {
	HostPath      string `yaml:"host_path"`
	ContainerPath string `yaml:"container_path"`
	ReadOnly      bool   `yaml:"read_only"`
}

type Sandbox struct {
	Use           string         `yaml:"use"`
	AllowHostBash bool           `yaml:"allow_host_bash"`
	Mounts        []SandboxMount `yaml:"mounts"`
}

type Title struct {
	Enabled   bool   `yaml:"enabled"`
	MaxWords  int    `yaml:"max_words"`
	MaxChars  int    `yaml:"max_chars"`
	ModelName string `yaml:"model_name"`
}

// SummarizationThreshold backs both `trigger:` (a list) and
// `keep:` (a single record). Value is float64 because legitimate
// configs mix integer counts (tokens/messages) with fractional
// ratios (`type: fraction, value: 0.8`).
type SummarizationThreshold struct {
	Type  string  `yaml:"type"`
	Value float64 `yaml:"value"`
}

type Summarization struct {
	Enabled                           bool                     `yaml:"enabled"`
	ModelName                         string                   `yaml:"model_name"`
	Trigger                           []SummarizationThreshold `yaml:"trigger"`
	Keep                              SummarizationThreshold   `yaml:"keep"`
	TrimTokensToSummarize             int                      `yaml:"trim_tokens_to_summarize"`
	SummaryPrompt                     string                   `yaml:"summary_prompt"`
	PreserveRecentSkillCount          int                      `yaml:"preserve_recent_skill_count"`
	PreserveRecentSkillTokens         int                      `yaml:"preserve_recent_skill_tokens"`
	PreserveRecentSkillTokensPerSkill int                      `yaml:"preserve_recent_skill_tokens_per_skill"`
	SkillFileReadToolNames            []string                 `yaml:"skill_file_read_tool_names"`
}

type Memory struct {
	Enabled                 bool    `yaml:"enabled"`
	StoragePath             string  `yaml:"storage_path"`
	DebounceSeconds         int     `yaml:"debounce_seconds"`
	ModelName               string  `yaml:"model_name"`
	MaxFacts                int     `yaml:"max_facts"`
	FactConfidenceThreshold float64 `yaml:"fact_confidence_threshold"`
	InjectionEnabled        bool    `yaml:"injection_enabled"`
	MaxInjectionTokens      int     `yaml:"max_injection_tokens"`
}

type AgentsAPI struct {
	Enabled bool `yaml:"enabled"`
}

type SkillEvolution struct {
	Enabled             bool   `yaml:"enabled"`
	ModerationModelName string `yaml:"moderation_model_name"`
}

type Checkpointer struct {
	Type             string `yaml:"type"`
	ConnectionString string `yaml:"connection_string"`
}

// UnmarshalYAML bridges the one wire/runtime mismatch that prevents
// Config from being a plain yaml.Unmarshal target: yaml's `models:`
// is a list of ModelEntry (with legacy aliases like api_base /
// timeout), but downstream wants map[string]*ModelConfig keyed by
// name. We capture the list locally, then run normalizeModels.
//
// All other fields ride through unchanged via the alias trick:
// `type alias Config` shares Config's underlying struct (and thus
// its yaml tags) but strips this method, avoiding infinite
// recursion. The inline-embedded alias decodes every yaml-tagged
// Config field; only `models:` is intercepted by the override
// below.
//
// Runtime fields on Config are tagged yaml:"-" and therefore
// untouched by this method — Load() fills them in after the YAML
// decode returns.
func (c *Config) UnmarshalYAML(node *yaml.Node) error {
	type alias Config
	aux := struct {
		alias  `yaml:",inline"`
		Models []ModelEntry `yaml:"models"`
	}{
		alias: alias(*c),
	}
	if err := node.Decode(&aux); err != nil {
		return err
	}
	*c = Config(aux.alias)
	c.Models = normalizeModels(aux.Models)
	return nil
}

// loadFromYAML reads and decodes config.yaml into a fresh Config.
// The decoder dispatches through Config.UnmarshalYAML, so the
// returned value already has Models in runtime-shape; runtime-only
// fields (RootDir, env-driven defaults) remain zero and get filled
// by Load.
func loadFromYAML(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read yaml config: %w", err)
	}

	var cfg Config
	if err = yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse yaml config: %w", err)
	}

	return &cfg, nil
}

// normalizeModels turns the YAML wire shape ([]ModelEntry, with
// legacy aliases like api_base / timeout) into the runtime shape
// (map[string]*ModelConfig, keyed by name). When the YAML doesn't
// declare any models, falls back to a built-in kimi default so the
// rest of normalizeConfig has something to validate.
func normalizeModels(entries []ModelEntry) map[string]*ModelConfig {
	if len(entries) == 0 {
		return map[string]*ModelConfig{
			defaultYAMLModel: {
				Name:           defaultYAMLModel,
				Provider:       "kimi",
				Model:          "moonshot-v1-8k",
				BaseURL:        "https://api.moonshot.cn/v1",
				APIKeyEnv:      "MOONSHOT_API_KEY",
				TimeoutSeconds: 60,
			},
		}
	}

	out := make(map[string]*ModelConfig, len(entries))
	for _, m := range entries {
		mc := ModelConfig{
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
		out[m.Name] = ToPtr(mc)
	}
	return out
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
