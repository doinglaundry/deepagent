# DeerFlow / Helixent / eino-cli 功能对照与迁移规划

## 背景

为评估当前 `eino-cli` 仓库的能力坐标、规划下一阶段功能演进，对两个参考
仓库做系统盘点：

- `deer-flow`（`/Users/bytedance/PycharmProjects/deer-flow`）：平台级
  super-agent。FastAPI gateway + LangGraph 运行时 + Next.js 前端 + 21 个
  公共 skill。多用户、多线程、Web 应用形态。
- `helixent`（`/Users/bytedance/PycharmProjects/helixent`）：单人开发者
  CLI，Cursor / Claude Code 风格。Bun + Ink/React TUI，单二进制分发。
- `eino-cli`（本仓库，模块名 `eino-cli`）：Bubble Tea TUI + CloudWeGo
  eino deep agent，Go 单进程 REPL，定位介于两者之间。

简言之：**deer-flow 是"平台"，helixent 是"工具"，eino-cli 是"工具但
已有平台型能力雏形"**。

---

## 1. 维度对比

### 1.1 Agent 运行时

| 能力 | deer-flow | helixent | eino-cli |
|------|-----------|----------|----------|
| 主 agent | `make_lead_agent`（LangGraph） | `createCodingAgent` | `MakeLeadAgent` |
| 中间件链 | 16+ 层（thread/uploads/guardrail/summarize/todo/memory/clarification…） | 4 层（approval/skills/todo/tool-error） | 13 层（已对齐 deer-flow 多数） |
| Plan mode | `is_plan_mode` 动态注入 todo writer | 通过 todo 工具自然推动 | **已实现但永久关闭**（`MakeLeadAgent` 传 `false`） |
| Subagent | `task` 工具 + `SubagentExecutor` 隔离执行 | 无（lead-only） | `SubagentLimit` 已限并发；`buildNamedSubagents` **仅测试引用** |
| Loop detection | `LoopDetectionMiddleware` | 无 | **已有** `middlewares.LoopDetection` |
| Guardrails | `GuardrailMiddleware` 政策拦截 | 无 | **无** |
| Title 生成 | `TitleMiddleware` | 无 | 中间件存在但 `OnFirstUserMessage` 未挂 hook |
| Checkpoint resume | 多后端（memory / sqlite / postgres） | 无 | 文件 checkpoint 已落盘但**无 resume API** |

### 1.2 工具体系

| 能力 | deer-flow | helixent | eino-cli |
|------|-----------|----------|----------|
| 文件工具 | read/write/edit/list/glob/grep | 同名 + apply_patch/mkdir/move_path/file_info | ls/read/write/edit/glob/grep/apply_patch/delete_file/rg/semantic_search/read_lints |
| Shell | `bash` | `bash` | `execute` + `shell`/`await_shell`（带任务 id 异步） |
| Web / research | Tavily / Exa / Firecrawl / Jina / InfoQuest | 无 | **无** |
| MCP | `_load_mcp_tools()` adapter | 无 | **无** |
| ACP | `invoke_acp_agent_tool` | 无 | **仅 prompt 说明，无工具实现** |
| Skill evolution | `skill_manage_tool` | 无 | **无**（prompt 占位） |
| Tool search / deferred | `tool_search` + `DeferredToolFilterMiddleware` | 无 | 中间件已实现，**prompt 端未完全触发** |
| 结果摘要 / 降噪 | `DeerFlowSummarizationMiddleware`（对话级） | `tool-result-policy` + `summarizeToolResultText`（每工具级） | **无统一 policy**，靠 TUI 端 `formatArgsLine` 摘要 |
| HITL 审批 | `ClarificationMiddleware` + tool | 三档（once / always-project / deny），项目级 settings.local.json 持久化 | y/n 两档，无持久化 |

### 1.3 Skills 系统

| 能力 | deer-flow | helixent | eino-cli |
|------|-----------|----------|----------|
| 格式 | SKILL.md + YAML frontmatter | SKILL.md + gray-matter | SKILL.md + YAML（已对齐） |
| 加载 | public + custom 合并 | 多目录扫描去重 | public + custom（已对齐） |
| 校验 | 严格白名单 + hyphen-case | 仅类型 | **已有** `ValidateFrontmatter` |
| 安全扫描 | `scan_skill_content` 写前异步扫描 | 无 | **无** |
| 进化 / 编辑 | `skill_manage_tool` agent 可写 | 无 | **无** |
| 提示注入 | XML `<available_skills>` 块 | XML `<skill_system>` 块 + 显式 hint | **已实现** XML 块 |
| 动态 slash 命令 | 前端 `?mode=skill` | `/skill-name` + `requestedSkillName` | **已实现** slash popup |
| HTTP API | `/api/skills` 装/卸/启 | 无 | **无** |

### 1.4 Memory

| 能力 | deer-flow | helixent | eino-cli |
|------|-----------|----------|----------|
| 捕获 | `MemoryMiddleware` 后台异步合并 | 无（仅 messages） | 已有 `Memory` middleware |
| 存储 | 文件后端，按 user / agent 分区 | 无 | JSON 文件，按 agent 分区（schema 已对齐 deer-flow） |
| HTTP CRUD | `/api/memory` | 无 | **无** |
| 注入 | system 注入 + 上限 | 无 | `<memory>` system block |
| AGENTS.md 注入 | 无显式 | 自动作为首条 user 消息 | soul.md 通过 bootstrap，**不等价** |

### 1.5 CLI / TUI 体验

| 能力 | deer-flow（前端） | helixent（Ink） | eino-cli（Bubble Tea） |
|------|------------------|------------------|------------------------|
| Slash command popup | Cmd+K command palette | Ink 弹窗 | **已实现**（builtin + skill） |
| 输入历史 | localStorage | `~/.helixent/history.txt` | `.eino-cli/history.txt` |
| 滚动 | DOM scrollarea | scrollback flush | **已实现** scrollback flush |
| Token count footer | `usage_metadata` 汇总 | assistant `usage.totalTokens` 累计 | **无** |
| Logo / header | 工作区品牌 | ASCII rabbit + cwd | banner（已 flush 到 scrollback） |
| Loading 文案 | 通用 shimmer | 多套随机文案 + shimmer | 单一 verb |
| HITL 审批 UI | clarification panel | 三档 + 项目持久化 | y/n + Esc，**无持久化** |
| Ask user question | confirmDialog | 多问题 tab + 多选 / preview | **无** |
| Plan mode 切换 | UI 顶部 toggle | 工具自然推动 | **无切换入口** |

### 1.6 平台型能力（多用户 / API / 部署）

| 能力 | deer-flow | helixent | eino-cli |
|------|-----------|----------|----------|
| HTTP gateway | FastAPI + SSE | 无 | **无** |
| 多用户 auth | JWT + CSRF + setup wizard | 无 | **无** |
| Threads / Runs | LangGraph 兼容 API | 无 | 进程内 history slice |
| Tracing | LangSmith / Langfuse | 无 | slog Trace event，**无导出** |
| IM channels | 飞书 / Slack 通道服务 | 无 | **无** |
| 单二进制分发 | Docker | Bun `--compile` | `go build` |
| Doctor / setup wizard | `scripts/doctor.py`、`setup_wizard.py` | Ink first-run wizard | **无** |

---

## 2. 可迁移功能清单

按 **改造成本 × 价值** 排序。

### A. 低成本 / 立刻可做（一两个 commit 收完）

1. **打开 Plan mode 入口**。`MakeLeadAgent` 当前硬编码 `IsPlanMode=false`；
   加 `/plan on|off` slash 或 yaml `default_plan_mode` 即可激活已写好的
   `Todo` 注入。
2. **Title 中间件接 hook**。`Title` 中间件已在链上但 `OnFirstUserMessage`
   是空，接一个 callback 就能产出对话标题，后续给 TUI footer / header 用。
3. **三档 HITL 审批 + 项目持久化**（helixent 风格）。扩展
   `agent.HITLApprover` 返回值，加 `allow_always_project`，写入
   `.eino-cli/settings.local.json`，TUI 渲染三选项。
4. **Token count footer**。`TokenUsage` 中间件已经统计；给 `Trace` 加一个
   token phase 让 TUI footer 显示累计 token。
5. **Loading 文案池**。helixent 风格 7 条随机 verb + shimmer，替换当前单一
   verb（TUI 已有 `pickVerb`，扩成池即可）。
6. **AGENTS.md 自动注入**。仿 helixent 在首轮 user message 前追加项目根
   `AGENTS.md`。我们已有 soul.md，但 AGENTS.md 是 Cursor / Codex 通用约定，
   值得对齐。

### B. 中等成本 / 高价值（一周内）

7. **统一 tool result policy**（helixent 的 `tool-result-policy` +
   `tool-result-runtime`）。当前 TUI 端 `formatArgsLine` 是临时方案，迁到
   agent 层后 model 看到的也是结构化、可截断的结果，能省 token + 防 prompt
   注入。
8. **Subagent task 工具实现**。`buildNamedSubagents` 已经存在但只跑测试。
   把 deer-flow `SubagentExecutor` 的 timeouts / status / journal 思路移植，
   绑成 `task` 工具。
9. **Ask user question 多选 UI**。现在 `ask_clarification` 只有一句话提问，
   迁 helixent 的多问题 tab + 多选 + preview，给 onboarding 体验加分。
10. **Doctor / health 子命令**。仿 `scripts/doctor.py`，加 `eino-cli doctor`
    检查 yaml、模型 key、skill 目录、checkpoint 目录。零依赖、用户痛点高。
11. **Skill evolution tool**。deer-flow 的 `skill_manage_tool` +
    `scan_skill_content` 安全扫描。agent 自主写 skill 是核心差异化能力，但
    需要 frontmatter 严格校验（已有）+ 安全扫描（无）。

### C. 大改造 / 战略性（独立 spec）

12. **HTTP gateway + SSE streaming**。deer-flow 的 `app/gateway`。这是从 CLI
    走向 platform 的关键，要先决定要不要做 multi-user。建议先做单用户
    HTTP + SSE，对接外部前端或 webhook。
13. **MCP 客户端**。deer-flow 通过 LangChain adapter 加载 MCP tools。Go 生态
    有 `mcp-go`，可接入。一旦有 MCP，外部工具生态全开。
14. **Tracing 导出**。把 `Trace` 中间件 events 桥接到 LangSmith / Langfuse /
    OpenTelemetry。platform 化绕不开。
15. **Thread / run 持久化**。`pendingCheckpointID` 已在但无 resume API；加
    `/resume <thread>` 命令 + thread CRUD，是从 in-process history slice 升级
    到 multi-thread 的第一步。

### D. 不建议迁移（性价比低 / 偏离定位）

- **Next.js 前端 / IM channels / 多用户 auth**：deer-flow 的"平台壳"，对纯
  CLI agent 没收益。
- **Web / research 工具（Tavily / Exa / Firecrawl / Jina / InfoQuest）**：
  可作为 plugin / MCP 后引入，不必内置。
- **单二进制 `--compile`**：Go 默认就出单二进制，无须新增。
- **Skill marketplace UI**：在 Web 体验下才合理，CLI 用
  `eino-cli skill install <url>` 命令即可，不必做 marketplace。

---

## 3. 建议的下一步

如果只能选三件事，按这个顺序：

1. **A1 + A4 + A5**：先把已经写完但没开关的能力点亮（plan mode、token
   footer、loading 文案、AGENTS.md），用户感知最强、改动最小。
2. **B7**：tool result policy 统一到 agent 层。这是 helixent 最值得抄的
   工程化能力，对长上下文成本影响大。
3. **B8**：subagent task 工具落地。这是从"单 agent CLI"变成"deep agent"的
   真正分水岭。

C 区项目（gateway / MCP / tracing）每个都值得单独写 design doc，不建议合到
一次开发里。

---

## 4. 关键引用

| 仓库 | 文件 / 符号 |
|------|------------|
| deer-flow lead agent | `backend/packages/harness/deerflow/agents/lead_agent/agent.py` — `make_lead_agent` |
| deer-flow tool 装配 | `backend/packages/harness/deerflow/tools/tools.py` — `get_available_tools` |
| deer-flow skill 管理 | `backend/packages/harness/deerflow/tools/skill_manage_tool.py`，`skills/security_scanner.py` |
| deer-flow gateway | `backend/app/gateway/app.py`，`routers/{threads,thread_runs,skills,memory,auth}.py` |
| helixent agent loop | `src/agent/agent.ts` — `Agent.stream` |
| helixent tool result | `src/agent/tool-result-policy.ts`，`tool-result-runtime.ts` |
| helixent skills | `src/agent/skills/skills-middleware.ts` |
| helixent 三档审批 | `src/coding/permissions/coding-approval-middleware.ts`，`src/cli/tui/components/approval-prompt.tsx` |
| eino-cli runtime | `backend/runtime/eino/deep_runtime.go` — `DeepAgentRuntime` |
| eino-cli middleware chain | `backend/agent/middleware_chain.go` — `GetChatModelMiddlewares` |
| eino-cli tool roster | `backend/agent/tools/tools.go` — `BuildBuiltinTools` |
| eino-cli skill 加载 | `backend/agent/skills/loader.go` |
| eino-cli TUI | `backend/cli/tui/{update,view,model,commands,popup}.go` |
