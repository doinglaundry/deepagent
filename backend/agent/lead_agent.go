package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"

	"eino-cli/backend/agent/middlewares"
	"eino-cli/backend/config"
	memorystore "eino-cli/backend/memory/store"
)

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
// rt is treated as immutable: every mutation (name canonicalization,
// model resolution, thinking-mode collapse, metadata seed + log line)
// lives in FinalizeRuntimeContext. Callers must call Finalize before
// MakeLeadAgent — both production entry points (NewDeepAgentRuntime
// and buildNamedSubagents) do so. MakeLeadAgent itself only consumes.
//
// MakeLeadAgent is also self-contained: it owns its sandbox (cwd-backed
// LocalSandbox) and its memory accessor (cfg.MemoryDir-backed store).
// Hosts that want different behaviour for either should swap in their
// own assembly function rather than threading deps through here — the
// extra DI layer was deleted in the previous revision because every
// production call site used identical defaults.
//
// The bootstrap branch from the Python original is intentionally
// omitted per the technical plan.
func MakeLeadAgent(
	ctx context.Context,
	rt RuntimeContext,
	cfg config.Config,
) (adk.ResumableAgent, error) {
	agentConfig, err := GetAgentConfig(cfg, rt.AgentName)
	if err != nil {
		return nil, fmt.Errorf("load agent profile %q: %w", rt.AgentName, err)
	}
	modelCfg := cfg.Models[rt.ModelName]

	chatModel, err := buildChatModel(ctx, *modelCfg, rt.ThinkingEnabled, rt.ReasoningEffort)
	if err != nil {
		return nil, err
	}
	summaryModel := buildSummaryChatModel(ctx, cfg, chatModel)

	sandbox := NewLocalSandbox("")
	mem := NewMemoryAccessor(memorystore.NewStore(cfg.MemoryDir))

	prompt := ApplyPromptTemplate(PromptOptions{
		SubagentEnabled:        rt.SubagentEnabled,
		MaxConcurrentSubagents: rt.MaxConcurrentSubagents,
		AgentName:              rt.AgentName,
		AvailableSkills:        skillsFromProfile(agentConfig),
		Config:                 cfg,
		Mounts:                 sandbox.Mounts(),
		Mem:                    mem,
	})

	chain, err := BuildChain(ctx, rt, cfg, summaryModel)
	if err != nil {
		return nil, fmt.Errorf("build middleware chain: %w", err)
	}

	withGeneral := generalSubagentEnabled(ctx, rt)

	deepCfg := &deep.Config{
		Name:         fallback(rt.AgentName, "deep-agent"),
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
	applyToolGroups(deepCfg, agentConfig, sandbox)

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
