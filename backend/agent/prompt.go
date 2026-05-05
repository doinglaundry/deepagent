// Package agent contains the lead-agent assembly logic ported from
// deerflow.agents.lead_agent. prompt.go is a 1:1 translation of
// deerflow.agents.lead_agent.prompt: it builds the system prompt fed to the
// chat model. External runtime dependencies (skills, subagents, memory, ACP,
// sandbox mounts) are abstracted via the PromptDeps struct so the Go side can
// stay decoupled from the Python deerflow runtime — every nil function maps
// to the corresponding Python try/except branch that returns "".
package agent

import (
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// -----------------------------------------------------------------------------
// Types — minimal mirrors of the Python objects used by the prompt assembler.
// -----------------------------------------------------------------------------

// Skill mirrors deerflow.skills.Skill (only fields used by the prompt).
type Skill struct {
	Name        string
	Description string
	Category    string // "custom" → marked [custom, editable]; otherwise [built-in]
	SkillFile   string
}

// SubagentConfig mirrors deerflow.subagents.registry.SubagentConfig.
type SubagentConfig struct {
	Description string
}

// Mount mirrors a sandbox mount entry.
type Mount struct {
	ContainerPath string
	ReadOnly      bool
}

// MemoryConfig mirrors deerflow's memory config block.
type MemoryConfig struct {
	Enabled            bool
	InjectionEnabled   bool
	MaxInjectionTokens int
}

// SkillEvolutionConfig mirrors deerflow's skill_evolution config block.
type SkillEvolutionConfig struct {
	Enabled bool
}

// ToolSearchConfig mirrors deerflow's tool_search config block.
type ToolSearchConfig struct {
	Enabled bool
}

// SandboxConfig holds custom mount info.
type SandboxConfig struct {
	Mounts []Mount
}

// SummarizationConfig captures the deerflow summarization knobs.
// Phase 2 wires this into the always-on middleware chain via
// middlewares.NewSummarization.
type SummarizationConfig struct {
	Enabled         bool
	ContextTokens   int
	ContextMessages int
	UserInstruction string
}

// TokenUsageConfig is the gate flag for the token-usage middleware (Phase 3).
type TokenUsageConfig struct {
	Enabled bool
}

// HumanInTheLoopConfig is the gate flag for the HITL middleware (Phase 3).
type HumanInTheLoopConfig struct {
	Enabled bool
}

// AppConfig mirrors deerflow.config.app_config.AppConfig
// (only the fields touched by the prompt assembler and middleware chain).
type AppConfig struct {
	Memory         MemoryConfig
	SkillEvolution SkillEvolutionConfig
	ToolSearch     ToolSearchConfig
	Sandbox        SandboxConfig
	Summarization  SummarizationConfig
	TokenUsage     TokenUsageConfig
	HumanInTheLoop HumanInTheLoopConfig
}

// DeferredEntry represents a tool_search deferred-tool registry entry.
type DeferredEntry struct {
	Name string
}

// PromptDeps abstracts every external runtime call the Python module makes.
// A nil function maps to Python's try/except branch (return ""), keeping the
// final prompt deterministic even when the surrounding runtime is missing.
type PromptDeps struct {
	LoadAgentSoul            func(agentName string) string
	LoadSkills               func() []Skill // enabled-only
	GetSubagentNames         func() []string
	GetSubagentConfig        func(name string) *SubagentConfig
	GetEffectiveUserID       func() string
	GetMemoryData            func(agentName, userID string) any
	FormatMemoryForInjection func(data any, maxTokens int) string
	GetDeferredRegistry      func() []DeferredEntry
	GetACPAgents             func() map[string]any
}

// AvailableSkills represents Python's `available_skills: set[str] | None`.
//
//   - All == true                       → mirrors Python `None` (load every enabled skill).
//   - All == false, Names == nil/empty  → mirrors Python `set()` (no skills).
//   - All == false, Names == [...]      → mirrors Python `set([...])`.
type AvailableSkills struct {
	All   bool
	Names []string
}

// AllSkills returns the sentinel meaning "load every enabled skill" — equivalent
// to passing available_skills=None in Python.
func AllSkills() *AvailableSkills { return &AvailableSkills{All: true} }

// SkillSet returns an AvailableSkills representing a Python set([names...]).
func SkillSet(names ...string) *AvailableSkills {
	return &AvailableSkills{All: false, Names: append([]string(nil), names...)}
}

// PromptOptions is the input to ApplyPromptTemplate.
type PromptOptions struct {
	SubagentEnabled        bool
	MaxConcurrentSubagents int
	AgentName              string
	AvailableSkills        *AvailableSkills // nil == AllSkills() == Python None
	AppConfig              *AppConfig
	Deps                   *PromptDeps
}

// -----------------------------------------------------------------------------
// Internal helpers
// -----------------------------------------------------------------------------

func nonNilDeps(d *PromptDeps) PromptDeps {
	if d == nil {
		return PromptDeps{}
	}
	return *d
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// availableSkillsAsSet returns (allSkills, names, isExplicit). isExplicit==false
// means the caller passed Python's None — load everything enabled.
func availableSkillsAsSet(a *AvailableSkills) (allSkills bool, names []string) {
	if a == nil || a.All {
		return true, nil
	}
	return false, a.Names
}

// -----------------------------------------------------------------------------
// _build_available_subagents_description
// -----------------------------------------------------------------------------

func buildAvailableSubagentsDescription(deps PromptDeps, availableNames []string, bashAvailable bool) string {
	bashDesc := "Not available in the current sandbox configuration. Use direct file/web tools or switch to AioSandboxProvider for isolated shell access."
	if bashAvailable {
		bashDesc = "For command execution (git, build, test, deploy operations)"
	}
	builtin := map[string]string{
		"general-purpose": "For ANY non-trivial task - web research, code exploration, file operations, analysis, etc.",
		"bash":            bashDesc,
	}

	var lines []string
	for _, name := range availableNames {
		if d, ok := builtin[name]; ok {
			lines = append(lines, fmt.Sprintf("- **%s**: %s", name, d))
			continue
		}
		if deps.GetSubagentConfig == nil {
			continue
		}
		cfg := deps.GetSubagentConfig(name)
		if cfg == nil {
			continue
		}
		first := strings.TrimSpace(strings.SplitN(cfg.Description, "\n", 2)[0])
		lines = append(lines, fmt.Sprintf("- **%s**: %s", name, first))
	}
	return strings.Join(lines, "\n")
}

// -----------------------------------------------------------------------------
// _build_subagent_section
// -----------------------------------------------------------------------------

func buildSubagentSection(deps PromptDeps, n int) string {
	var availableNames []string
	if deps.GetSubagentNames != nil {
		availableNames = deps.GetSubagentNames()
	}
	bashAvailable := contains(availableNames, "bash")

	availableSubagents := buildAvailableSubagentsDescription(deps, availableNames, bashAvailable)

	directToolExamples := "ls, read_file, web_search, etc."
	if bashAvailable {
		directToolExamples = "bash, ls, read_file, web_search, etc."
	}

	var directExecutionExample string
	if bashAvailable {
		directExecutionExample = "# User asks: \"Run the tests\"\n# Thinking: Cannot decompose into parallel sub-tasks\n# → Execute directly\n\nbash(\"npm test\")  # Direct execution, not task()"
	} else {
		directExecutionExample = "# User asks: \"Read the README\"\n# Thinking: Single straightforward file read\n# → Execute directly\n\nread_file(\"workspace/README.md\")  # Direct execution, not task()"
	}

	lines := []string{
		"<subagent_system>",
		"**🚀 SUBAGENT MODE ACTIVE - DECOMPOSE, DELEGATE, SYNTHESIZE**",
		"",
		"You are running with subagent capabilities enabled. Your role is to be a **task orchestrator**:",
		"1. **DECOMPOSE**: Break complex tasks into parallel sub-tasks",
		"2. **DELEGATE**: Launch multiple subagents simultaneously using parallel `task` calls",
		"3. **SYNTHESIZE**: Collect and integrate results into a coherent answer",
		"",
		"**CORE PRINCIPLE: Complex tasks should be decomposed and distributed across multiple subagents for parallel execution.**",
		"",
		fmt.Sprintf("**⛔ HARD CONCURRENCY LIMIT: MAXIMUM %d `task` CALLS PER RESPONSE. THIS IS NOT OPTIONAL.**", n),
		fmt.Sprintf("- Each response, you may include **at most %d** `task` tool calls. Any excess calls are **silently discarded** by the system — you will lose that work.", n),
		"- **Before launching subagents, you MUST count your sub-tasks in your thinking:**",
		fmt.Sprintf("  - If count ≤ %d: Launch all in this response.", n),
		fmt.Sprintf("  - If count > %d: **Pick the %d most important/foundational sub-tasks for this turn.** Save the rest for the next turn.", n, n),
		fmt.Sprintf("- **Multi-batch execution** (for >%d sub-tasks):", n),
		fmt.Sprintf("  - Turn 1: Launch sub-tasks 1-%d in parallel → wait for results", n),
		"  - Turn 2: Launch next batch in parallel → wait for results",
		"  - ... continue until all sub-tasks are complete",
		"  - Final turn: Synthesize ALL results into a coherent answer",
		fmt.Sprintf("- **Example thinking pattern**: \"I identified 6 sub-tasks. Since the limit is %d per turn, I will launch the first %d now, and the rest in the next turn.\"", n, n),
		"",
		"**Available Subagents:**",
		availableSubagents,
		"",
		"**Your Orchestration Strategy:**",
		"",
		"✅ **DECOMPOSE + PARALLEL EXECUTION (Preferred Approach):**",
		"",
		fmt.Sprintf("For complex queries, break them down into focused sub-tasks and execute in parallel batches (max %d per turn):", n),
		"",
		"**Example 1: \"Why is Tencent's stock price declining?\" (3 sub-tasks → 1 batch)**",
		"→ Turn 1: Launch 3 subagents in parallel:",
		"- Subagent 1: Recent financial reports, earnings data, and revenue trends",
		"- Subagent 2: Negative news, controversies, and regulatory issues",
		"- Subagent 3: Industry trends, competitor performance, and market sentiment",
		"→ Turn 2: Synthesize results",
		"",
		"**Example 2: \"Compare 5 cloud providers\" (5 sub-tasks → multi-batch)**",
		fmt.Sprintf("→ Turn 1: Launch %d subagents in parallel (first batch)", n),
		"→ Turn 2: Launch remaining subagents in parallel",
		"→ Final turn: Synthesize ALL results into comprehensive comparison",
		"",
		"**Example 3: \"Refactor the authentication system\"**",
		"→ Turn 1: Launch 3 subagents in parallel:",
		"- Subagent 1: Analyze current auth implementation and technical debt",
		"- Subagent 2: Research best practices and security patterns",
		"- Subagent 3: Review related tests, documentation, and vulnerabilities",
		"→ Turn 2: Synthesize results",
		"",
		fmt.Sprintf("✅ **USE Parallel Subagents (max %d per turn) when:**", n),
		"- **Complex research questions**: Requires multiple information sources or perspectives",
		"- **Multi-aspect analysis**: Task has several independent dimensions to explore",
		"- **Large codebases**: Need to analyze different parts simultaneously",
		"- **Comprehensive investigations**: Questions requiring thorough coverage from multiple angles",
		"",
		"❌ **DO NOT use subagents (execute directly) when:**",
		"- **Task cannot be decomposed**: If you can't break it into 2+ meaningful parallel sub-tasks, execute directly",
		"- **Ultra-simple actions**: Read one file, quick edits, single commands",
		"- **Need immediate clarification**: Must ask user before proceeding",
		"- **Meta conversation**: Questions about conversation history",
		"- **Sequential dependencies**: Each step depends on previous results (do steps yourself sequentially)",
		"",
		"**CRITICAL WORKFLOW** (STRICTLY follow this before EVERY action):",
		"1. **COUNT**: In your thinking, list all sub-tasks and count them explicitly: \"I have N sub-tasks\"",
		fmt.Sprintf("2. **PLAN BATCHES**: If N > %d, explicitly plan which sub-tasks go in which batch:", n),
		fmt.Sprintf("   - \"Batch 1 (this turn): first %d sub-tasks\"", n),
		"   - \"Batch 2 (next turn): next batch of sub-tasks\"",
		fmt.Sprintf("3. **EXECUTE**: Launch ONLY the current batch (max %d `task` calls). Do NOT launch sub-tasks from future batches.", n),
		"4. **REPEAT**: After results return, launch the next batch. Continue until all batches complete.",
		"5. **SYNTHESIZE**: After ALL batches are done, synthesize all results.",
		fmt.Sprintf("6. **Cannot decompose** → Execute directly using available tools (%s)", directToolExamples),
		"",
		fmt.Sprintf("**⛔ VIOLATION: Launching more than %d `task` calls in a single response is a HARD ERROR. The system WILL discard excess calls and you WILL lose work. Always batch.**", n),
		"",
		"**Remember: Subagents are for parallel decomposition, not for wrapping single tasks.**",
		"",
		"**How It Works:**",
		"- The task tool runs subagents asynchronously in the background",
		"- The backend automatically polls for completion (you don't need to poll)",
		"- The tool call will block until the subagent completes its work",
		"- Once complete, the result is returned to you directly",
		"",
		fmt.Sprintf("**Usage Example 1 - Single Batch (≤%d sub-tasks):**", n),
		"",
		"```python",
		"# User asks: \"Why is Tencent's stock price declining?\"",
		"# Thinking: 3 sub-tasks → fits in 1 batch",
		"",
		"# Turn 1: Launch 3 subagents in parallel",
		"task(description=\"Tencent financial data\", prompt=\"...\", subagent_type=\"general-purpose\")",
		"task(description=\"Tencent news & regulation\", prompt=\"...\", subagent_type=\"general-purpose\")",
		"task(description=\"Industry & market trends\", prompt=\"...\", subagent_type=\"general-purpose\")",
		"# All 3 run in parallel → synthesize results",
		"```",
		"",
		fmt.Sprintf("**Usage Example 2 - Multiple Batches (>%d sub-tasks):**", n),
		"",
		"```python",
		"# User asks: \"Compare AWS, Azure, GCP, Alibaba Cloud, and Oracle Cloud\"",
		fmt.Sprintf("# Thinking: 5 sub-tasks → need multiple batches (max %d per batch)", n),
		"",
		fmt.Sprintf("# Turn 1: Launch first batch of %d", n),
		"task(description=\"AWS analysis\", prompt=\"...\", subagent_type=\"general-purpose\")",
		"task(description=\"Azure analysis\", prompt=\"...\", subagent_type=\"general-purpose\")",
		"task(description=\"GCP analysis\", prompt=\"...\", subagent_type=\"general-purpose\")",
		"",
		"# Turn 2: Launch remaining batch (after first batch completes)",
		"task(description=\"Alibaba Cloud analysis\", prompt=\"...\", subagent_type=\"general-purpose\")",
		"task(description=\"Oracle Cloud analysis\", prompt=\"...\", subagent_type=\"general-purpose\")",
		"",
		"# Turn 3: Synthesize ALL results from both batches",
		"```",
		"",
		"**Counter-Example - Direct Execution (NO subagents):**",
		"",
		"```python",
		directExecutionExample,
		"```",
		"",
		"**CRITICAL**:",
		fmt.Sprintf("- **Max %d `task` calls per turn** - the system enforces this, excess calls are discarded", n),
		"- Only use `task` when you can launch 2+ subagents in parallel",
		"- Single task = No value from subagents = Execute directly",
		fmt.Sprintf("- For >%d sub-tasks, use sequential batches of %d across multiple turns", n, n),
		"</subagent_system>",
	}
	return strings.Join(lines, "\n")
}

// -----------------------------------------------------------------------------
// _get_memory_context
// -----------------------------------------------------------------------------

// getMemoryContext mirrors deerflow.agents.lead_agent.prompt._get_memory_context.
// Python wraps the body in try/except and returns "" on any failure; the Go
// equivalent is "every dep is nil-safe → return ''".
func getMemoryContext(agentName string, deps PromptDeps, app *AppConfig) string {
	if app == nil || deps.GetMemoryData == nil || deps.FormatMemoryForInjection == nil {
		return ""
	}
	cfg := app.Memory
	if !cfg.Enabled || !cfg.InjectionEnabled {
		return ""
	}
	userID := ""
	if deps.GetEffectiveUserID != nil {
		userID = deps.GetEffectiveUserID()
	}
	memoryData := deps.GetMemoryData(agentName, userID)
	memoryContent := deps.FormatMemoryForInjection(memoryData, cfg.MaxInjectionTokens)
	if strings.TrimSpace(memoryContent) == "" {
		return ""
	}
	return "<memory>\n" + memoryContent + "\n</memory>\n"
}

// -----------------------------------------------------------------------------
// get_skills_prompt_section
// -----------------------------------------------------------------------------

// GetSkillsPromptSection mirrors get_skills_prompt_section.
func GetSkillsPromptSection(available *AvailableSkills, deps PromptDeps, app *AppConfig) string {
	var skills []Skill
	if deps.LoadSkills != nil {
		skills = deps.LoadSkills()
	}

	skillEvolutionEnabled := false
	if app != nil {
		skillEvolutionEnabled = app.SkillEvolution.Enabled
	}

	if len(skills) == 0 && !skillEvolutionEnabled {
		return ""
	}

	allSkills, allowedNames := availableSkillsAsSet(available)

	if !allSkills {
		anyMatch := false
		for _, s := range skills {
			if contains(allowedNames, s.Name) {
				anyMatch = true
				break
			}
		}
		if !anyMatch {
			return ""
		}
	}

	var filtered []Skill
	if allSkills {
		filtered = skills
	} else {
		for _, s := range skills {
			if contains(allowedNames, s.Name) {
				filtered = append(filtered, s)
			}
		}
	}

	skillsXML := ""
	if len(filtered) > 0 {
		items := make([]string, 0, len(filtered))
		for _, s := range filtered {
			tag := "[built-in]"
			if s.Category == "custom" {
				tag = "[custom, editable]"
			}
			items = append(items, fmt.Sprintf(
				"    <skill>\n        <name>%s</name>\n        <description>%s %s</description>\n        <location>%s</location>\n    </skill>",
				s.Name, s.Description, tag, s.SkillFile,
			))
		}
		skillsXML = "<available_skills>\n" + strings.Join(items, "\n") + "\n</available_skills>"
	}

	skillEvolutionSection := ""
	if skillEvolutionEnabled {
		skillEvolutionSection = "" +
			"\n## Skill Self-Evolution\n" +
			"After completing a task, consider creating or updating a skill when:\n" +
			"- The task required 5+ tool calls to resolve\n" +
			"- You overcame non-obvious errors or pitfalls\n" +
			"- The user corrected your approach and the corrected version worked\n" +
			"- You discovered a non-trivial, recurring workflow\n" +
			"If you used a skill and encountered issues not covered by it, patch it immediately.\n" +
			"Prefer patch over edit. Before creating a new skill, confirm with the user first.\n" +
			"Skip simple one-off tasks.\n"
	}

	return "" +
		"<skill_system>\n" +
		"You have access to skills that provide optimized workflows for specific tasks. " +
		"Each skill contains best practices, frameworks, and references to additional resources.\n" +
		"\n" +
		"**Progressive Loading Pattern:**\n" +
		"1. When a user query matches a skill's use case, immediately call `read_file` on the skill's main file " +
		"using the path attribute provided in the skill tag below\n" +
		"2. Read and understand the skill's workflow and instructions\n" +
		"3. The skill file contains references to external resources under the same folder\n" +
		"4. Load referenced resources only when needed during execution\n" +
		"5. Follow the skill's instructions precisely\n" +
		"\n" +
		skillEvolutionSection + "\n" +
		skillsXML + "\n" +
		"\n" +
		"</skill_system>"
}

// -----------------------------------------------------------------------------
// get_agent_soul
// -----------------------------------------------------------------------------

// GetAgentSoul mirrors get_agent_soul.
func GetAgentSoul(agentName string, deps PromptDeps) string {
	if deps.LoadAgentSoul == nil {
		return ""
	}
	soul := deps.LoadAgentSoul(agentName)
	if soul == "" {
		return ""
	}
	return "<soul>\n" + soul + "\n</soul>\n"
}

// -----------------------------------------------------------------------------
// get_deferred_tools_prompt_section
// -----------------------------------------------------------------------------

// GetDeferredToolsPromptSection mirrors get_deferred_tools_prompt_section.
func GetDeferredToolsPromptSection(deps PromptDeps, app *AppConfig) string {
	if app == nil || !app.ToolSearch.Enabled {
		return ""
	}
	if deps.GetDeferredRegistry == nil {
		return ""
	}
	entries := deps.GetDeferredRegistry()
	if len(entries) == 0 {
		return ""
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}
	return "<available-deferred-tools>\n" + strings.Join(names, "\n") + "\n</available-deferred-tools>"
}

// -----------------------------------------------------------------------------
// _build_acp_section
// -----------------------------------------------------------------------------

func buildACPSection(deps PromptDeps) string {
	if deps.GetACPAgents == nil {
		return ""
	}
	agents := deps.GetACPAgents()
	if len(agents) == 0 {
		return ""
	}
	return "" +
		"\n**ACP Agent Tasks (invoke_acp_agent):**\n" +
		"- ACP agents (e.g. codex, claude_code) run in their own independent workspace — NOT in workspace/uploads/outputs\n" +
		"- When writing prompts for ACP agents, describe the task only — do NOT reference workspace/uploads/outputs paths\n" +
		"- ACP agent results are accessible at `/mnt/acp-workspace/` (read-only) — use `ls`, `read_file`, or `bash cp` to retrieve output files\n" +
		"- To deliver ACP output to the user: copy from `/mnt/acp-workspace/<file>` to `outputs/<file>`, then use `present_files`"
}

// -----------------------------------------------------------------------------
// _build_custom_mounts_section
// -----------------------------------------------------------------------------

func buildCustomMountsSection(app *AppConfig) string {
	if app == nil {
		return ""
	}
	mounts := app.Sandbox.Mounts
	if len(mounts) == 0 {
		return ""
	}
	lines := make([]string, 0, len(mounts))
	for _, m := range mounts {
		access := "read-write"
		if m.ReadOnly {
			access = "read-only"
		}
		lines = append(lines, fmt.Sprintf(
			"- Custom mount: `%s` - Host directory mapped into the sandbox (%s)",
			m.ContainerPath, access,
		))
	}
	return "\n**Custom Mounted Directories:**\n" + strings.Join(lines, "\n") +
		"\n- If the user needs files outside workspace/uploads/outputs, use these absolute container paths directly when they match the requested directory"
}

// -----------------------------------------------------------------------------
// SYSTEM_PROMPT_TEMPLATE
//
// This is the verbatim port of Python's SYSTEM_PROMPT_TEMPLATE. Go raw strings
// cannot contain backticks, so the template stores every backtick as the
// sentinel "§" (U+00A7) — confirmed absent from the upstream template — and
// substitutes them back at package init time via strings.ReplaceAll.
// -----------------------------------------------------------------------------

const systemPromptTemplateRaw = `
<role>
You are {agent_name}, an open-source super agent.
</role>

{soul}
{memory_context}

<thinking_style>
- Think concisely and strategically about the user's request BEFORE taking action
- Break down the task: What is clear? What is ambiguous? What is missing?
- **PRIORITY CHECK: If anything is unclear, missing, or has multiple interpretations, you MUST ask for clarification FIRST - do NOT proceed with work**
{subagent_thinking}- Never write down your full final answer or report in thinking process, but only outline
- CRITICAL: After thinking, you MUST provide your actual response to the user. Thinking is for planning, the response is for delivery.
- Your response must contain the actual answer, not just a reference to what you thought about
</thinking_style>

<clarification_system>
**WORKFLOW PRIORITY: CLARIFY → PLAN → ACT**
1. **FIRST**: Analyze the request in your thinking - identify what's unclear, missing, or ambiguous
2. **SECOND**: If clarification is needed, call §ask_clarification§ tool IMMEDIATELY - do NOT start working
3. **THIRD**: Only after all clarifications are resolved, proceed with planning and execution

**CRITICAL RULE: Clarification ALWAYS comes BEFORE action. Never start working and clarify mid-execution.**

**MANDATORY Clarification Scenarios - You MUST call ask_clarification BEFORE starting work when:**

1. **Missing Information** (§missing_info§): Required details not provided
   - Example: User says "create a web scraper" but doesn't specify the target website
   - Example: "Deploy the app" without specifying environment
   - **REQUIRED ACTION**: Call ask_clarification to get the missing information

2. **Ambiguous Requirements** (§ambiguous_requirement§): Multiple valid interpretations exist
   - Example: "Optimize the code" could mean performance, readability, or memory usage
   - Example: "Make it better" is unclear what aspect to improve
   - **REQUIRED ACTION**: Call ask_clarification to clarify the exact requirement

3. **Approach Choices** (§approach_choice§): Several valid approaches exist
   - Example: "Add authentication" could use JWT, OAuth, session-based, or API keys
   - Example: "Store data" could use database, files, cache, etc.
   - **REQUIRED ACTION**: Call ask_clarification to let user choose the approach

4. **Risky Operations** (§risk_confirmation§): Destructive actions need confirmation
   - Example: Deleting files, modifying production configs, database operations
   - Example: Overwriting existing code or data
   - **REQUIRED ACTION**: Call ask_clarification to get explicit confirmation

5. **Suggestions** (§suggestion§): You have a recommendation but want approval
   - Example: "I recommend refactoring this code. Should I proceed?"
   - **REQUIRED ACTION**: Call ask_clarification to get approval

**STRICT ENFORCEMENT:**
- ❌ DO NOT start working and then ask for clarification mid-execution - clarify FIRST
- ❌ DO NOT skip clarification for "efficiency" - accuracy matters more than speed
- ❌ DO NOT make assumptions when information is missing - ALWAYS ask
- ❌ DO NOT proceed with guesses - STOP and call ask_clarification first
- ✅ Analyze the request in thinking → Identify unclear aspects → Ask BEFORE any action
- ✅ If you identify the need for clarification in your thinking, you MUST call the tool IMMEDIATELY
- ✅ After calling ask_clarification, execution will be interrupted automatically
- ✅ Wait for user response - do NOT continue with assumptions

**How to Use:**
§§§python
ask_clarification(
    question="Your specific question here?",
    clarification_type="missing_info",  # or other type
    context="Why you need this information",  # optional but recommended
    options=["option1", "option2"]  # optional, for choices
)
§§§

**Example:**
User: "Deploy the application"
You (thinking): Missing environment info - I MUST ask for clarification
You (action): ask_clarification(
    question="Which environment should I deploy to?",
    clarification_type="approach_choice",
    context="I need to know the target environment for proper configuration",
    options=["development", "staging", "production"]
)
[Execution stops - wait for user response]

User: "staging"
You: "Deploying to staging..." [proceed]
</clarification_system>

{skills_section}

{deferred_tools_section}

{subagent_section}

<working_directory existed="true">
You have access to three directories for file operations:
- **uploads**: files uploaded by the user (read-only, automatically listed in context)
- **workspace**: your working area for temporary and intermediate files
- **outputs**: final deliverables — anything you want the user to receive must go here

Use these alias paths with the §read_file§, §write_file§, §ls§, and §view_image§ tools:
- uploads → §uploads/<filename>§
- workspace → §workspace/<filename>§
- outputs → §outputs/<filename>§
- relative paths (e.g. §README.md§) default to §workspace/§

**File Management:**
- Uploaded files are automatically listed in the <uploaded_files> section before each request
- Use §read_file§ to read uploaded files using their paths from the list
- For PDF, PPT, Excel, and Word files, converted Markdown versions (*.md) are available alongside originals
- Do all temporary work in the workspace directory
- Prefer alias paths (§workspace/...§, §uploads/...§, §outputs/...§) in tool calls
- Absolute filesystem paths are also supported when explicitly provided by the user
- Never use legacy §/mnt/user-data/...§ paths
- Final deliverables must be saved to the outputs directory and presented using §present_files§ tool
{acp_section}
</working_directory>

<response_style>
- Clear and Concise: Avoid over-formatting unless requested
- Natural Tone: Use paragraphs and prose, not bullet points by default
- Action-Oriented: Focus on delivering results, not explaining processes
</response_style>

<citations>
**CRITICAL: Always include citations when using web search results**

- **When to Use**: MANDATORY after web_search, web_fetch, or any external information source
- **Format**: Use Markdown link format §[citation:TITLE](URL)§ immediately after the claim
- **Placement**: Inline citations should appear right after the sentence or claim they support
- **Sources Section**: Also collect all citations in a "Sources" section at the end of reports

**Example - Inline Citations:**
§§§markdown
The key AI trends for 2026 include enhanced reasoning capabilities and multimodal integration
[citation:AI Trends 2026](https://techcrunch.com/ai-trends).
Recent breakthroughs in language models have also accelerated progress
[citation:OpenAI Research](https://openai.com/research).
§§§

**Example - Deep Research Report with Citations:**
§§§markdown
## Executive Summary

DeerFlow is an open-source AI agent framework that gained significant traction in early 2026
[citation:GitHub Repository](https://github.com/bytedance/deer-flow). The project focuses on
providing a production-ready agent system with sandbox execution and memory management
[citation:DeerFlow Documentation](https://deer-flow.dev/docs).

## Key Analysis

### Architecture Design

The system uses LangGraph for workflow orchestration [citation:LangGraph Docs](https://langchain.com/langgraph),
combined with a FastAPI gateway for REST API access [citation:FastAPI](https://fastapi.tiangolo.com).

## Sources

### Primary Sources
- [GitHub Repository](https://github.com/bytedance/deer-flow) - Official source code and documentation
- [DeerFlow Documentation](https://deer-flow.dev/docs) - Technical specifications

### Media Coverage
- [AI Trends 2026](https://techcrunch.com/ai-trends) - Industry analysis
§§§

**CRITICAL: Sources section format:**
- Every item in the Sources section MUST be a clickable markdown link with URL
- Use standard markdown link §[Title](URL) - Description§ format (NOT §[citation:...]§ format)
- The §[citation:Title](URL)§ format is ONLY for inline citations within the report body
- ❌ WRONG: §GitHub 仓库 - 官方源代码和文档§ (no URL!)
- ❌ WRONG in Sources: §[citation:GitHub Repository](url)§ (citation prefix is for inline only!)
- ✅ RIGHT in Sources: §[GitHub Repository](https://github.com/bytedance/deer-flow) - 官方源代码和文档

**WORKFLOW for Research Tasks:**
1. Use web_search to find sources → Extract {title, url, snippet} from results
2. Write content with inline citations: §claim [citation:Title](url)§
3. Collect all citations in a "Sources" section at the end
4. NEVER write claims without citations when sources are available

**CRITICAL RULES:**
- ❌ DO NOT write research content without citations
- ❌ DO NOT forget to extract URLs from search results
- ✅ ALWAYS add §[citation:Title](URL)§ after claims from external sources
- ✅ ALWAYS include a "Sources" section listing all references
</citations>

<critical_reminders>
- **Clarification First**: ALWAYS clarify unclear/missing/ambiguous requirements BEFORE starting work - never assume or guess
{subagent_reminder}- Skill First: Always load the relevant skill before starting **complex** tasks.
- Progressive Loading: Load resources incrementally as referenced in skills
- Output Files: Final deliverables must be in §outputs/§
- Clarity: Be direct and helpful, avoid unnecessary meta-commentary
- Including Images and Mermaid: Images and Mermaid diagrams are always welcomed in the Markdown format, and you're encouraged to use §![Image Description](image_path)

§ or "§§§mermaid" to display images in response or Markdown files
- Multi-task: Better utilize parallel tool calling to call multiple tools at one time for better performance
- Language Consistency: Keep using the same language as user's
- Always Respond: Your thinking is internal. You MUST always provide a visible response to the user after thinking.
</critical_reminders>
`

// systemPromptTemplate is the runtime-resolved template (with § replaced by `).
var systemPromptTemplate = strings.ReplaceAll(systemPromptTemplateRaw, "§", "`")

// -----------------------------------------------------------------------------
// apply_prompt_template (entry point)
// -----------------------------------------------------------------------------

// ApplyPromptTemplate mirrors deerflow.agents.lead_agent.prompt.apply_prompt_template.
// It assembles the system prompt by:
//  1. Resolving every dynamic section (memory, skills, subagent, ACP, mounts).
//  2. Substituting the named placeholders inside SYSTEM_PROMPT_TEMPLATE.
//  3. Appending the current date footer in the same "%Y-%m-%d, %A" format.
func ApplyPromptTemplate(opts PromptOptions) string {
	deps := nonNilDeps(opts.Deps)

	memoryContext := getMemoryContext(opts.AgentName, deps, opts.AppConfig)

	n := opts.MaxConcurrentSubagents
	subagentSection := ""
	if opts.SubagentEnabled {
		subagentSection = buildSubagentSection(deps, n)
	}

	subagentReminder := ""
	if opts.SubagentEnabled {
		subagentReminder = "" +
			"- **Orchestrator Mode**: You are a task orchestrator - decompose complex tasks into parallel sub-tasks. " +
			fmt.Sprintf("**HARD LIMIT: max %d `task` calls per response.** ", n) +
			fmt.Sprintf("If >%d sub-tasks, split into sequential batches of ≤%d. Synthesize after ALL batches complete.\n", n, n)
	}

	subagentThinking := ""
	if opts.SubagentEnabled {
		subagentThinking = "" +
			"- **DECOMPOSITION CHECK: Can this task be broken into 2+ parallel sub-tasks? If YES, COUNT them. " +
			fmt.Sprintf("If count > %d, you MUST plan batches of ≤%d and only launch the FIRST batch now. ", n, n) +
			fmt.Sprintf("NEVER launch more than %d `task` calls in one response.**\n", n)
	}

	skillsSection := GetSkillsPromptSection(opts.AvailableSkills, deps, opts.AppConfig)
	deferredToolsSection := GetDeferredToolsPromptSection(deps, opts.AppConfig)

	acpSection := buildACPSection(deps)
	customMountsSection := buildCustomMountsSection(opts.AppConfig)
	var nonEmpty []string
	if acpSection != "" {
		nonEmpty = append(nonEmpty, acpSection)
	}
	if customMountsSection != "" {
		nonEmpty = append(nonEmpty, customMountsSection)
	}
	acpAndMountsSection := strings.Join(nonEmpty, "\n")

	agentName := opts.AgentName
	if agentName == "" {
		agentName = "DeerFlow 2.0"
	}

	soul := GetAgentSoul(opts.AgentName, deps)

	replacer := strings.NewReplacer(
		"{agent_name}", agentName,
		"{soul}", soul,
		"{memory_context}", memoryContext,
		"{subagent_thinking}", subagentThinking,
		"{skills_section}", skillsSection,
		"{deferred_tools_section}", deferredToolsSection,
		"{subagent_section}", subagentSection,
		"{subagent_reminder}", subagentReminder,
		"{acp_section}", acpAndMountsSection,
	)
	prompt := replacer.Replace(systemPromptTemplate)

	return prompt + "\n<current_date>" + time.Now().Format("2006-01-02, Monday") + "</current_date>"
}

// -----------------------------------------------------------------------------
// Logging hook (parity with Python's logger.exception in
// _build_custom_mounts_section / _get_memory_context).
// -----------------------------------------------------------------------------

// promptLogger is exposed so callers can swap in a project-wide slog handler.
// Code paths that mirror Python's `logger.exception` write through this.
var promptLogger = slog.Default()
