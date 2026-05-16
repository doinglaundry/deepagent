package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

const defaultYAMLModel = "kimi"

// ModelEntry is the wire shape; legacy aliases (api_base/timeout) get
// normalised into the canonical ModelConfig fields.
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

// The types below mirror every top-level section of yaml/config.yaml so the
// file shape has one authoritative declaration. Unused sections still parse.

type TokenUsage struct {
	Enabled bool `yaml:"enabled"`
}

// ToolObservability gates the ToolCallObservability middleware (§5 of
// rebuild_builtin_tools spec). Disabled → middleware short-circuits to a
// no-op endpoint passthrough, zero per-call overhead.
type ToolObservability struct {
	Enabled bool `yaml:"enabled"`
}

type ToolBlocks struct {
	Enabled      bool `yaml:"enabled"`
	PreviewLines int  `yaml:"preview_lines"`
	ArgsMaxChars int  `yaml:"args_max_chars"`
	configured   bool
}

func (tb *ToolBlocks) UnmarshalYAML(value *yaml.Node) error {
	type rawToolBlocks ToolBlocks
	var raw rawToolBlocks
	if err := value.Decode(&raw); err != nil {
		return err
	}
	*tb = ToolBlocks(raw)
	tb.configured = true
	return nil
}

func (tb ToolBlocks) Configured() bool {
	return tb.configured
}

type ToolGroup struct {
	Name string `yaml:"name"`
}

// Tool captures heterogeneous `tools:` entries; per-tool variants land in Extra.
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

type Title struct {
	Enabled   bool   `yaml:"enabled"`
	MaxWords  int    `yaml:"max_words"`
	MaxChars  int    `yaml:"max_chars"`
	ModelName string `yaml:"model_name"`
}

// SummarizationThreshold mixes integer counts and fractional ratios; hence float64.
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
	Enabled                   bool    `yaml:"enabled"`
	StoragePath               string  `yaml:"storage_path"`
	DebounceSeconds           int     `yaml:"debounce_seconds"`
	ModelName                 string  `yaml:"model_name"`
	MaxFacts                  int     `yaml:"max_facts"`
	FactConfidenceThreshold   float64 `yaml:"fact_confidence_threshold"`
	InjectionEnabled          bool    `yaml:"injection_enabled"`
	MaxInjectionTokens        int     `yaml:"max_injection_tokens"`
	DedupEnabled              bool    `yaml:"dedup_enabled"`
	EpisodicDefaultTTLSeconds int     `yaml:"episodic_default_ttl_seconds"`
}

// WebSearch wires the local web_search function tool to a real backend.
// Disabled by default so network egress stays opt-in.
type WebSearch struct {
	Enabled        bool   `yaml:"enabled"`
	Provider       string `yaml:"provider"` // bocha (only one supported today)
	BaseURL        string `yaml:"base_url"`
	APIKey         string `yaml:"api_key"`
	APIKeyEnv      string `yaml:"api_key_env"`
	MaxResults     int    `yaml:"max_results"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
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

// ErrorHandling gates the LLM-call wrapper that classifies transport errors,
// retries transient/busy with capped exponential backoff, and trips a circuit
// breaker after N consecutive failures. Enabled is the master switch — when
// false, the wrapper is skipped and the raw model error propagates.
type ErrorHandling struct {
	Enabled        bool                 `yaml:"enabled"`
	Retry          RetryConfig          `yaml:"retry"`
	CircuitBreaker CircuitBreakerConfig `yaml:"circuit_breaker"`
}

type RetryConfig struct {
	MaxAttempts int `yaml:"max_attempts"`
	BaseDelayMS int `yaml:"base_delay_ms"`
	CapDelayMS  int `yaml:"cap_delay_ms"`
}

type CircuitBreakerConfig struct {
	FailureThreshold int `yaml:"failure_threshold"`
	RecoverySeconds  int `yaml:"recovery_seconds"`
}

// UnmarshalYAML intercepts `models:` (list → map); everything else flows through
// the `type alias Config` trick to avoid infinite recursion. Runtime fields
// (yaml:"-") stay zero and are filled by Load().
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

// loadFromYAML decodes config.yaml; Models come back in runtime shape.
func loadFromYAML(root string) (*Config, error) {

	data, err := os.ReadFile(filepath.Join(root, "yaml", "config.yaml"))
	if err != nil {
		return nil, fmt.Errorf("read yaml config: %w", err)
	}

	var config Config
	if err = yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parse yaml config: %w", err)
	}

	config.RootDir = root

	config.DefaultAgent = defaultAgentKey

	config.Agents = map[string]*AgentConfig{
		defaultAgentKey: {
			Name:         defaultAgentKey,
			Instruction:  defaultAgentInstruction,
			MaxIteration: defaultAgentIterations,
			Model:        config.DefaultModel,
		},
	}

	skillFolderPath := filepath.Join(root, "backend", "skills")
	if !slices.Contains(config.Skills.Paths, skillFolderPath) {
		config.Skills.Paths = append(config.Skills.Paths, skillFolderPath)
	}

	return &config, nil
}

// normalizeModels converts []ModelEntry → map[name]*ModelConfig; falls back
// to a built-in kimi default when the YAML declares none.
func normalizeModels(entries []ModelEntry) map[string]*ModelConfig {
	if len(entries) == 0 {
		return map[string]*ModelConfig{
			defaultYAMLModel: {
				Name:           defaultYAMLModel,
				Provider:       "kimi",
				Model:          "moonshot-v1-8k",
				BaseURL:        "https://api.moonshot.cn/v1",
				APIKey:         os.Getenv("MOONSHOT_API_KEY"),
				TimeoutSeconds: 60,
			},
		}
	}

	out := make(map[string]*ModelConfig, len(entries))
	for _, m := range entries {
		modelCfg := ModelConfig{
			Name:     m.Name,
			Provider: m.Provider,
			Model:    m.Model,
		}
		if m.BaseURL != "" {
			modelCfg.BaseURL = m.BaseURL
		} else if m.APIBase != "" {
			modelCfg.BaseURL = m.APIBase
		}
		// API-key precedence: api_key_env → api_key:$VAR → literal.
		switch {
		case m.APIKeyEnv != "":
			modelCfg.APIKey = os.Getenv(m.APIKeyEnv)
		case strings.HasPrefix(m.APIKey, "$"):
			modelCfg.APIKey = os.Getenv(strings.TrimPrefix(m.APIKey, "$"))
		case strings.TrimSpace(m.APIKey) != "":
			modelCfg.APIKey = strings.TrimSpace(m.APIKey)
		}
		if m.TimeoutSeconds > 0 {
			modelCfg.TimeoutSeconds = m.TimeoutSeconds
		} else if m.Timeout > 0 {
			modelCfg.TimeoutSeconds = int(m.Timeout)
		}
		if modelCfg.Provider == "" {
			modelCfg.Provider = inferProvider(modelCfg.BaseURL, m.Name)
		}
		out[m.Name] = ToPtr(modelCfg)
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
