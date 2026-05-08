package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"

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

	// WorkingDir is consulted only when Sandbox is nil; ignored otherwise.
	WorkingDir string

	// HITLTools and HITLApprovalFunc drive the human-in-the-loop
	// middleware. HITLTools is the set of tool names that require
	// approval; empty means no gating. HITLApprovalFunc is the callback
	// that prompts the user — it receives the tool name + raw JSON args
	// and returns approve/deny. nil callback treats every gated call as
	// approved (Phase 1 behavior).
	HITLTools        []string
	HITLApprovalFunc func(ctx context.Context, toolName, args string) bool

	// OnClarificationFunc, if non-nil, is invoked when the model emits
	// an ask_clarification tool call. The middleware always rewrites
	// the assistant message to surface the question; this callback
	// gives the host a hook for telemetry / custom rendering.
	OnClarificationFunc func(ctx context.Context, question string)

	// DeferredToolNamesFunc returns the live list of tool names the
	// DeferredTools middleware should filter out of the active set when
	// cfg.ToolSearch.Enabled is true. Without this the middleware
	// is not attached.
	DeferredToolNamesFunc func() []string

	// MemoryHooks drives the Memory middleware's inject / extract data
	// plane. Wire only when cfg.Memory.Enabled is true. (Not a single
	// func — this is a struct bundling Inject + Extract callbacks, so
	// it does not get the Func suffix.)
	MemoryHooks middlewares.MemoryHooks

	// MemoryFlushHookFunc is plugged into the summarization middleware
	// so the host can persist memorable bits before/around
	// summarization. Optional; nil means "no flush hook" — the
	// middleware skips the callback entirely.
	MemoryFlushHookFunc middlewares.SummarizationMemoryFlushHook
}

// MakeLeadAgent mirrors deerflow.agents.lead_agent.agent.make_lead_agent.
//
// The Python flow is:
//  1. Read RunnableConfig → resolve model/agent names → build chat model
//  2. Render the system prompt via apply_prompt_template
//  3. Build the middleware chain via _build_middlewares
//  4. Hand everything to langchain.agents.create_agent
//
// In Go we substitute step 4 with deep.New (which already gives us the
// same loop semantics: tool calling, max-iteration cap, checkpoint
// support, filesystem subagent tools). The remaining steps line up
// 1:1.
//
// The bootstrap branch from the Python original is intentionally
// omitted per the technical plan.
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

	agentConfig, err := GetAgentConfig(cfg, agentName)
	if err != nil {
		return nil, fmt.Errorf("load agent profile %q: %w", agentName, err)
	}

	modelName, modelCfg, err := GetModelConfig(rt.ModelName, agentConfig, cfg)
	if err != nil {
		return nil, err
	}
	// Pin the resolved model name back onto rt so any downstream
	// helper that reads cfg.Models[rt.ModelName] sees the same entry
	// GetModelConfig picked (the fallback path may differ from the
	// raw rt.ModelName the caller supplied).
	rt.ModelName = modelName

	thinkingEnabled := getThinkingEnabled(rt.ThinkingEnabled, modelCfg, modelName)
	populateRuntimeMetadata(&rt, agentName, modelName, thinkingEnabled, agentConfig)

	chatModel, err := buildChatModel(ctx, *modelCfg, thinkingEnabled, rt.ReasoningEffort)
	if err != nil {
		return nil, err
	}
	summaryModel := buildSummaryChatModel(ctx, cfg, chatModel)

	if deps.Sandbox == nil {
		deps.Sandbox = NewLocalSandbox(deps.WorkingDir)
	}

	prompt := ApplyPromptTemplate(PromptOptions{
		SubagentEnabled:        rt.SubagentEnabled,
		MaxConcurrentSubagents: rt.MaxConcurrentSubagents,
		AgentName:              agentName,
		AvailableSkills:        skillsFromProfile(agentConfig),
		Config:                 cfg,
		Mounts:                 deps.Sandbox.Mounts(),
		Deps:                   deps.PromptDeps,
	})

	chain, err := BuildChain(ctx, rt, cfg, deps, summaryModel)
	if err != nil {
		return nil, fmt.Errorf("build middleware chain: %w", err)
	}

	withGeneral := generalSubagentEnabled(ctx, rt)

	deepCfg := &deep.Config{
		Name:         fallback(agentName, "deep-agent"),
		Description:  "Deep Agent",
		ChatModel:    chatModel,
		Instruction:  prompt,
		MaxIteration: defaultIterationLimit(agentConfig),
		// Phase 10: driven by rt.SubagentEnabled (the sole gate after
		// the SubagentsConfig YAML knob was removed).
		WithoutGeneralSubAgent: !withGeneral,
		// Phase 8: write_todos is always available so the agent can
		// self-elect to track multi-step work even outside plan mode —
		// matching Cursor / Claude Code. The plan-mode-only nudge
		// still lives in the Todo middleware (chain.Agent), gated on
		// rt.IsPlanMode.
		WithoutWriteTodos: false,
		Middlewares:       chain.Agent,
		Handlers:          chain.ChatModel,
	}
	// Phase 9: honour profile.ToolGroups (deerflow's
	// get_available_tools(groups=...) filter). nil ToolGroups → inherit
	// all (Backend + Shell stay wired); explicit slice → opt-in only.
	applyToolGroups(deepCfg, agentConfig, deps.Sandbox)

	agentImpl, err := deep.New(ctx, deepCfg)
	if err != nil {
		return nil, fmt.Errorf("build deep agent: %w", err)
	}
	return agentImpl, nil
}

// -----------------------------------------------------------------------------
// Orchestration helpers (collocated with MakeLeadAgent because they
// only exist to keep its body short and have no other call sites).
// -----------------------------------------------------------------------------

// populateRuntimeMetadata logs the "Create Agent" line and seeds
// rt.Metadata with the same fields so downstream middleware /
// renderers can inspect them. rt.Metadata is mutated in place (maps
// are reference types).
func populateRuntimeMetadata(rt *RuntimeContext, agentName, modelName string, thinkingEnabled bool, profile *config.AgentConfig) {
	resolvedName := fallback(agentName, "default")
	resolvedModel := fallback(modelName, "default")

	slog.Info("Create Agent",
		"agent_name", resolvedName,
		"thinking_enabled", thinkingEnabled,
		"reasoning_effort", rt.ReasoningEffort,
		"model_name", resolvedModel,
		"is_plan_mode", rt.IsPlanMode,
		"subagent_enabled", rt.SubagentEnabled,
		"max_concurrent_subagents", rt.MaxConcurrentSubagents,
	)

	if rt.Metadata == nil {
		rt.Metadata = map[string]any{}
	}
	rt.Metadata["agent_name"] = resolvedName
	rt.Metadata["model_name"] = resolvedModel
	rt.Metadata["thinking_enabled"] = thinkingEnabled
	rt.Metadata["reasoning_effort"] = rt.ReasoningEffort
	rt.Metadata["is_plan_mode"] = rt.IsPlanMode
	rt.Metadata["subagent_enabled"] = rt.SubagentEnabled
	if profile != nil {
		rt.Metadata["tool_groups"] = profile.ToolGroups
		if profile.Skills != nil {
			rt.Metadata["available_skills"] = profile.Skills
		}
	}
}

// discoverImageFetcher checks whether the sandbox provider can read
// image bytes (optional ImageReader capability) and returns the
// fetcher the ViewImage middleware expects. Returns nil when the
// sandbox doesn't expose ReadImage; the middleware silently skips in
// that case.
func discoverImageFetcher(sandbox SandboxProvider) middlewares.ImageFetcher {
	r, ok := sandbox.(ImageReader)
	if !ok {
		return nil
	}
	return imageFetcherFunc(r.ReadImage)
}

// imageFetcherFunc adapts a plain ReadImage method into the
// middlewares.ImageFetcher interface so MakeLeadAgent doesn't have to
// declare a dedicated wrapper type per provider.
type imageFetcherFunc func(ctx context.Context, path string) ([]byte, string, error)

func (f imageFetcherFunc) ReadImage(ctx context.Context, path string) ([]byte, string, error) {
	return f(ctx, path)
}

// -----------------------------------------------------------------------------
// Tiny shared utilities
// -----------------------------------------------------------------------------

// fallback returns def when value is empty / whitespace-only, else value.
func fallback(value, def string) string {
	if strings.TrimSpace(value) == "" {
		return def
	}
	return value
}

// skillsFromProfile maps an AgentConfig.Skills declaration onto the
// AvailableSkills value the prompt template understands. nil profile
// or nil Skills slice → "load all enabled" (Python: None); a non-nil
// slice (even empty) → strict subset.
func skillsFromProfile(p *config.AgentConfig) *AvailableSkills {
	if p == nil || p.Skills == nil {
		return AllSkills()
	}
	return SkillSet(p.Skills...)
}

// defaultIterationLimit picks the per-turn cap on the agent loop.
//
// Resolution order mirrors deerflow's lead_agent.make_lead_agent:
//   - profile.MaxIteration (when > 0) — per-agent override from
//     config.yaml or agents/<name>/config.yaml.
//   - runtimeMaxIterDefault (6) — inherited from the original
//     DeepAgentRuntime default; matches Python's hardcoded fallback.
//
// Negative values are clamped to the default to avoid a configuration
// typo turning into "agent never runs". 0 explicitly means "inherit
// the default" (matches the YAML zero value).
func defaultIterationLimit(p *config.AgentConfig) int {
	const runtimeMaxIterDefault = 6
	if p == nil || p.MaxIteration <= 0 {
		return runtimeMaxIterDefault
	}
	return p.MaxIteration
}
