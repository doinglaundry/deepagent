package config

// This file holds the public, runtime-facing type definitions
// for the config package. It used to live as its own
// `eino-cli/backend/config/schema` subpackage so downstream
// packages (agent, runtime/eino) could depend on the types
// without pulling in the YAML/env loader. In practice every
// downstream importer already pulls in the parent `config`
// package via type aliases (`config.Config = schema.Config`,
// etc.), so the split was generating duplication for no
// isolation benefit. Merging the types into `config` makes
// "one config package, one source of truth" the rule.
//
// Conventions for new types:
//   - Public, runtime-facing — used by code outside `config`
//     (lead agent, runtime factory, prompt deps, ...).
//     Internal-only YAML wire shapes belong in yaml.go alongside
//     the loader.
//   - Carry `json` tags for the runtime view, plus `yaml` tags
//     when they're embedded directly into FileConfig (i.e. when
//     YAML and runtime shape happen to coincide — see
//     SkillsConfig and ToolSearchConfig for examples).

type ModelConfig struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	BaseURL  string `json:"base_url,omitempty"`

	// APIKey holds a literal credential pulled directly from the
	// YAML (the `api_key: sk-...` form). Prefer APIKeyEnv for
	// shared / source-controlled configs — literal keys here will
	// be persisted to disk in plain text.
	APIKey string `json:"api_key,omitempty"`

	// APIKeyEnv is the env-var name to read at runtime (the
	// `api_key: $MOONSHOT_API_KEY` or `api_key_env: MOONSHOT_API_KEY`
	// form). Used as the fallback when APIKey is empty.
	APIKeyEnv      string `json:"api_key_env,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`

	// SupportsThinking declares whether the provider/model accepts the
	// extended-thinking toggle. Mirrors the deerflow ModelConfig field of the
	// same name; lead_agent.MakeLeadAgent uses it to decide whether to honor
	// RuntimeContext.ThinkingEnabled or silently downgrade.
	SupportsThinking bool `json:"supports_thinking,omitempty"`

	// ThinkingBudgetTokens sets the per-request "extended thinking" budget
	// for providers that honour it (Claude). When 0 and thinking is
	// enabled, the chat model factory uses a sensible default (4096).
	ThinkingBudgetTokens int `json:"thinking_budget_tokens,omitempty"`

	// SupportsVision declares whether the model can ingest image content.
	// The agent.middlewares.ViewImage middleware uses this to decide whether
	// to enable the vision routing path. Mirrors deerflow's same-named flag.
	SupportsVision bool `json:"supports_vision,omitempty"`
}

// AgentConfig describes a custom agent. Mirrors deerflow's
// AgentConfig dataclass: a top-level YAML record under either
// `agents:` (inline map) or `<root>/agents/<name>/config.yaml`
// (per-agent directory).
//
// Tags are dual: json for the inline cfg.Agents map (parsed via the
// outer Config's json round-trip in tests) and yaml for the per-agent
// "<agents_dir>/<name>/config.yaml" loader. This lets one type carry
// both wire formats so the agent package can drop its private
// agentYAMLFile mirror.
type AgentConfig struct {
	Name         string `json:"name"                    yaml:"name"`
	Description  string `json:"description,omitempty"   yaml:"description,omitempty"`
	Instruction  string `json:"instruction,omitempty"   yaml:"instruction,omitempty"`
	MaxIteration int    `json:"max_iteration,omitempty" yaml:"max_iteration,omitempty"`

	// Model overrides the global default model when non-empty.
	Model string `json:"model,omitempty" yaml:"model,omitempty"`

	// ToolGroups restricts the active tool surface for this agent.
	// nil means "inherit the lead-agent default"; an explicit empty
	// slice means "no tools" (advanced use case).
	ToolGroups []string `json:"tool_groups,omitempty" yaml:"tool_groups,omitempty"`

	// Skills behaves like ToolGroups: nil = inherit, [] = strict empty,
	// non-empty = subset selection. Mirrors Python deerflow semantics
	// for the prompt's <available_skills> section.
	Skills []string `json:"skills,omitempty" yaml:"skills,omitempty"`
}

// SkillsConfig drives the SKILL.md scanner used to populate the
// <available_skills> prompt section. Each path is expanded with ~
// and either scanned one level deep ("<name>/SKILL.md", legacy
// flat layout) or two levels deep ("public|custom/<name>/SKILL.md",
// deerflow layout — picked automatically when the directory has a
// public/ or custom/ subdir).
//
// Enabled maps skill name -> on/off. Mirrors deerflow's
// extensions_config.json `skills` map but is co-located here so
// LLM can stay single-file-config. Unlisted skills default to
// enabled, matching deerflow's "public/custom default true" rule.
type SkillsConfig struct {
	Paths   []string        `json:"paths,omitempty"   yaml:"paths,omitempty"`
	Enabled map[string]bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`
}

// DeferredToolEntry describes a tool that is registered but not loaded by
// default — the agent has to opt in through the deferred-tool prompt section.
type DeferredToolEntry struct {
	Name        string `json:"name"                  yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// ToolSearchConfig gates the deferred-tool prompt section + the
// DeferredTools middleware that filters those tools out of the active set.
type ToolSearchConfig struct {
	Enabled  bool                `json:"enabled"            yaml:"enabled"`
	Deferred []DeferredToolEntry `json:"deferred,omitempty" yaml:"deferred,omitempty"`
}

// ACPAgentEntry captures the prompt-side metadata for an external ACP
// agent (codex, claude_code, ...). The Description is what the LLM sees
// when deciding whether to invoke the agent.
type ACPAgentEntry struct {
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// ACPConfig holds the registered ACP agents whose mere presence flips on
// the ACP prompt subsection.
type ACPConfig struct {
	Agents map[string]ACPAgentEntry `json:"agents,omitempty" yaml:"agents,omitempty"`
}

// Config is the application's single source of truth, carrying both
// runtime-derived fields (from os.Getwd / env vars / defaults) and
// YAML-sourced fields (from yaml/config.yaml) in one struct. Runtime
// fields are tagged yaml:"-" so the decoder leaves them alone;
// YAML-sourced fields use Config.UnmarshalYAML (see yaml.go), which
// also handles the wire-shape translation for `models:`.
//
// Only the sections currently consumed downstream live here. YAML
// keys not declared on this struct are silently ignored by yaml.v3,
// which is fine for documentation-only sections in the file
// (config_version, log_level, token_usage, tool_groups, tools,
// uploads, sandbox, title, summarization, memory, agents_api,
// skill_evolution, checkpointer). Wiring any of them up means
// adding the field here AND routing it through the relevant
// builder (BuildAppConfig, BuildPromptDeps, ...).
type Config struct {
	RootDir        string `json:"root_dir"        yaml:"-"`
	PersistenceDir string `json:"persistence_dir" yaml:"-"`
	SessionsDir    string `json:"sessions_dir"    yaml:"-"`
	MemoryDir      string `json:"memory_dir"      yaml:"-"`
	CheckpointDir  string `json:"checkpoint_dir"  yaml:"-"`

	RuntimeTimeout int `json:"runtime_timeout" yaml:"-"`

	DefaultAgent string                 `json:"default_agent" yaml:"-"`
	Agents       map[string]AgentConfig `json:"agents"        yaml:"-"`
	ACP          ACPConfig              `json:"acp,omitempty" yaml:"-"`

	DefaultModel string                  `json:"default_model"         yaml:"default_model"`
	Models       map[string]*ModelConfig `json:"models"                yaml:"-"` // built from the YAML list via UnmarshalYAML + normalizeModels
	ToolSearch   ToolSearchConfig        `json:"tool_search,omitempty" yaml:"tool_search"`
	Skills       SkillsConfig            `json:"skills,omitempty"      yaml:"skills"`
}
