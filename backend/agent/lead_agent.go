package agent

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	claudemodel "github.com/cloudwego/eino-ext/components/model/claude"
	openaimodel "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/components/model"

	"eino-cli/backend/agent/middlewares"
	"eino-cli/backend/config"
)

// AgentDeps bundles the host-supplied capabilities that don't live in
// config: a sandbox (filesystem + shell + mounts) and the per-call
// PromptDeps (the same one ApplyPromptTemplate consumes).
//
// The split mirrors deerflow's distinction between "config" (declarative)
// and "runtime" (host implementations).
type AgentDeps struct {
	// Sandbox owns Backend / Shell / Mounts. If nil, MakeLeadAgent falls
	// back to NewLocalSandbox(WorkingDir) — the same behaviour eino-cli
	// shipped with before Phase 4.
	Sandbox SandboxProvider

	PromptDeps *PromptDeps
	AppConfig  *AppConfig

	// WorkingDir is consulted only when Sandbox is nil; ignored otherwise.
	WorkingDir string

	// HITLTools and HITLApproval drive the human-in-the-loop middleware.
	// HITLTools is the set of tool names that require approval; empty
	// means no gating. HITLApproval is the callback that prompts the user
	// — it receives the tool name + raw JSON args and returns approve/deny.
	// nil callback treats every gated call as approved (Phase 1 behavior).
	HITLTools    []string
	HITLApproval func(ctx context.Context, toolName, args string) bool

	// OnClarification, if non-nil, is invoked when the model emits an
	// ask_clarification tool call. The middleware always rewrites the
	// assistant message to surface the question; this callback gives the
	// host a hook for telemetry / custom rendering.
	OnClarification func(ctx context.Context, question string)

	// DeferredToolNames is the live list of tool names the
	// DeferredTools middleware should filter out of the active set when
	// AppConfig.ToolSearch.Enabled is true. Without this the middleware
	// is not attached.
	DeferredToolNames func() []string

	// MemoryHooks drives the Memory middleware's inject / extract data
	// plane. Wire only when AppConfig.Memory.Enabled is true.
	MemoryHooks middlewares.MemoryHooks
}

// MakeLeadAgent mirrors deerflow.agents.lead_agent.agent.make_lead_agent.
//
// The Python flow is:
//  1. Read RunnableConfig → resolve model/agent names → build chat model
//  2. Render the system prompt via apply_prompt_template
//  3. Build the middleware chain via _build_middlewares
//  4. Hand everything to langchain.agents.create_agent
//
// In Go we substitute step 4 with deep.New (which already gives us the same
// loop semantics: tool calling, max-iteration cap, checkpoint support,
// filesystem subagent tools). The remaining steps line up 1:1.
//
// The bootstrap branch from the Python original is intentionally omitted
// per the technical plan.
func MakeLeadAgent(
	ctx context.Context,
	rt RuntimeContext,
	cfg config.Config,
	deps AgentDeps,
) (adk.ResumableAgent, error) {
	agentName, err := ValidateAgentName(rt.AgentName)
	if err != nil {
		return nil, err
	}

	profile, err := LoadAgentProfile(cfg, agentName)
	if err != nil {
		return nil, fmt.Errorf("load agent profile %q: %w", agentName, err)
	}

	modelName, modelCfg, err := ResolveModelForAgent(rt.ModelName, profile, cfg)
	if err != nil {
		return nil, err
	}

	thinkingEnabled := rt.ThinkingEnabled
	if thinkingEnabled && !modelCfg.SupportsThinking {
		slog.Warn("thinking enabled but model does not support it; downgrading",
			"model", modelName)
		thinkingEnabled = false
	}

	slog.Info("Create Agent",
		"agent_name", fallback(agentName, "default"),
		"thinking_enabled", thinkingEnabled,
		"reasoning_effort", rt.ReasoningEffort,
		"model_name", modelName,
		"is_plan_mode", rt.IsPlanMode,
		"subagent_enabled", rt.SubagentEnabled,
		"max_concurrent_subagents", rt.MaxConcurrentSubagents,
	)

	if rt.Metadata == nil {
		rt.Metadata = map[string]any{}
	}
	rt.Metadata["agent_name"] = fallback(agentName, "default")
	rt.Metadata["model_name"] = fallback(modelName, "default")
	rt.Metadata["thinking_enabled"] = thinkingEnabled
	rt.Metadata["reasoning_effort"] = rt.ReasoningEffort
	rt.Metadata["is_plan_mode"] = rt.IsPlanMode
	rt.Metadata["subagent_enabled"] = rt.SubagentEnabled
	if profile != nil {
		rt.Metadata["tool_groups"] = profile.ToolGroups
	}
	if profile != nil && profile.Skills != nil {
		rt.Metadata["available_skills"] = profile.Skills
	}

	chatModel, err := buildChatModel(ctx, *modelCfg, thinkingEnabled, rt.ReasoningEffort)
	if err != nil {
		return nil, err
	}

	sandbox := deps.Sandbox
	if sandbox == nil {
		sandbox = NewLocalSandbox(deps.WorkingDir)
	}

	// Surface sandbox mounts into the prompt's "Custom Mounted Directories"
	// section. We layer them on top of any AppConfig.Sandbox.Mounts the host
	// configured statically — matching deerflow's behaviour where the
	// runtime-provided list takes precedence.
	appCfg := deps.AppConfig
	if mounts := sandbox.Mounts(); len(mounts) > 0 {
		appCopy := AppConfig{}
		if appCfg != nil {
			appCopy = *appCfg
		}
		appCopy.Sandbox.Mounts = append(append([]Mount(nil), appCopy.Sandbox.Mounts...), mounts...)
		appCfg = &appCopy
	}

	prompt := ApplyPromptTemplate(PromptOptions{
		SubagentEnabled:        rt.SubagentEnabled,
		MaxConcurrentSubagents: rt.MaxConcurrentSubagents,
		AgentName:              agentName,
		AvailableSkills:        skillsFromProfile(profile),
		AppConfig:              appCfg,
		Deps:                   deps.PromptDeps,
	})

	// If the sandbox can read images, expose it as the ViewImage
	// middleware's fetcher. Sandboxes without that capability silently
	// degrade — the middleware logs and skips when no fetcher is wired.
	var imageFetcher middlewares.ImageFetcher
	if r, ok := sandbox.(ImageReader); ok {
		imageFetcher = imageFetcherFunc(r.ReadImage)
	}

	chain, err := BuildChain(ctx, ChainOptions{
		Runtime:           rt,
		ModelName:         modelName,
		AgentName:         agentName,
		ModelConfig:       modelCfg,
		Config:            cfg,
		AppConfig:         appCfg,
		SummaryModel:      chatModel,
		HITLTools:         deps.HITLTools,
		HITLApproval:      deps.HITLApproval,
		OnClarification:   deps.OnClarification,
		DeferredToolNames: deps.DeferredToolNames,
		MemoryHooks:       deps.MemoryHooks,
		ImageFetcher:      imageFetcher,
	})
	if err != nil {
		return nil, fmt.Errorf("build middleware chain: %w", err)
	}

	maxIter := defaultIterationLimit(profile)

	agentImpl, err := deep.New(ctx, &deep.Config{
		Name:                   fallback(agentName, "deep-agent"),
		Description:            "Deep Agent",
		ChatModel:              chatModel,
		Instruction:            prompt,
		Backend:                sandbox.Backend(),
		Shell:                  sandbox.Shell(),
		MaxIteration:           maxIter,
		WithoutGeneralSubAgent: true,
		// Phase 8: write_todos is now always available so the agent can
		// self-elect to track multi-step work even outside plan mode —
		// matching the way Cursor / Claude Code expose the same tool.
		// The plan-mode-only "use this tool" rallying-cry still lives in
		// the Todo middleware (chain.Agent), gated on rt.IsPlanMode.
		WithoutWriteTodos: false,
		Middlewares:       chain.Agent,
		Handlers:          chain.ChatModel,
	})
	if err != nil {
		return nil, fmt.Errorf("build deep agent: %w", err)
	}
	return agentImpl, nil
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

func fallback(value, def string) string {
	if strings.TrimSpace(value) == "" {
		return def
	}
	return value
}

func skillsFromProfile(p *AgentProfile) *AvailableSkills {
	if p == nil || p.Skills == nil {
		return AllSkills() // Python: available_skills=None
	}
	return SkillSet(p.Skills...)
}

func defaultIterationLimit(p *AgentProfile) int {
	// Phase 3 will surface MaxIteration on AgentProfile; for now use the
	// existing DeepAgentRuntime default so REPL behaviour is unchanged.
	_ = p
	return 6
}

// imageFetcherFunc adapts a plain ReadImage method into the
// middlewares.ImageFetcher interface so MakeLeadAgent doesn't have to
// declare a dedicated wrapper type per provider.
type imageFetcherFunc func(ctx context.Context, path string) ([]byte, string, error)

func (f imageFetcherFunc) ReadImage(ctx context.Context, path string) ([]byte, string, error) {
	return f(ctx, path)
}

// buildChatModel is the agent-package chat model factory.
//
// Mirrors deerflow's create_chat_model(name, thinking_enabled, reasoning_effort):
// the lead-agent assembly resolves both flags from the RuntimeContext and
// hands them in here so the actual API client is constructed with the
// right knobs. Earlier phases shipped a flag-blind version that lost
// these settings before the request ever left the process.
//
// thinkingEnabled is honoured by Claude (extended-thinking; budget comes
// from cfg.ThinkingBudgetTokens or a 4096 default). reasoningEffort is
// honoured by OpenAI (low/medium/high → openai.ReasoningEffortLevel).
// Kimi/Moonshot ignore both — neither is in the upstream API surface.
func buildChatModel(
	ctx context.Context,
	cfg config.ModelConfig,
	thinkingEnabled bool,
	reasoningEffort string,
) (model.BaseChatModel, error) {
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	apiKey := strings.TrimSpace(os.Getenv(strings.TrimSpace(cfg.APIKeyEnv)))
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second

	switch provider {
	case "claude", "anthropic":
		claudeCfg := &claudemodel.Config{
			Model:     strings.TrimSpace(cfg.Model),
			MaxTokens: 2048,
			APIKey:    apiKey,
		}
		if timeout > 0 {
			claudeCfg.HTTPClient = &http.Client{Timeout: timeout}
		}
		if baseURL := strings.TrimSpace(cfg.BaseURL); baseURL != "" {
			claudeCfg.BaseURL = &baseURL
		}
		if thinkingEnabled {
			budget := cfg.ThinkingBudgetTokens
			if budget <= 0 {
				budget = 4096
			}
			// Claude requires MaxTokens > BudgetTokens; bump if too small.
			if claudeCfg.MaxTokens <= budget {
				claudeCfg.MaxTokens = budget + 1024
			}
			claudeCfg.Thinking = &claudemodel.Thinking{
				Enable:       true,
				BudgetTokens: budget,
			}
		}
		return claudemodel.NewChatModel(ctx, claudeCfg)
	case "openai":
		return openaimodel.NewChatModel(ctx, &openaimodel.ChatModelConfig{
			APIKey:          apiKey,
			Model:           strings.TrimSpace(cfg.Model),
			BaseURL:         strings.TrimSpace(cfg.BaseURL),
			Timeout:         timeout,
			ReasoningEffort: parseReasoningEffort(reasoningEffort),
		})
	case "kimi", "moonshot":
		baseURL := strings.TrimSpace(cfg.BaseURL)
		if baseURL == "" {
			baseURL = "https://api.moonshot.cn/v1"
		}
		modelName := strings.TrimSpace(cfg.Model)
		if !strings.HasPrefix(strings.ToLower(modelName), "moonshot") {
			modelName = "moonshot-v1-8k"
		}
		return openaimodel.NewChatModel(ctx, &openaimodel.ChatModelConfig{
			APIKey:  apiKey,
			Model:   modelName,
			BaseURL: baseURL,
			Timeout: timeout,
		})
	default:
		return nil, fmt.Errorf("unsupported model provider %q", cfg.Provider)
	}
}

// parseReasoningEffort maps the textual effort knob coming from
// RuntimeContext / RunnableConfig onto the typed enum the OpenAI client
// expects. An empty / unknown value falls through as the zero value
// (== "no override"), matching Python's behaviour where a missing
// reasoning_effort lets the upstream default apply.
func parseReasoningEffort(s string) openaimodel.ReasoningEffortLevel {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "low":
		return openaimodel.ReasoningEffortLevelLow
	case "medium":
		return openaimodel.ReasoningEffortLevelMedium
	case "high":
		return openaimodel.ReasoningEffortLevelHigh
	default:
		return ""
	}
}

