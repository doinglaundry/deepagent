# 砍 RuntimeContext + 收尾 7 处断链 — 技术方案

> 日期: 2026-05-13
> 上下文: 工作区已经在做"砍 `RuntimeContext` 间接层"的重构,中间态留下了若干断链(B1-B7 + D1-D2)。本 doc 把剩余收尾全部落地。

---

## 0. 决策总览

参照 AGENTS.md "项目特例:配置只用一个 `config.Config`" + "**结构体只装数据。函数承载行为**" + "**少压调用栈**"。

| 项 | 决策 | 落点 |
|---|---|---|
| `RuntimeContext` 类型 + setter / Clone | **整体删** | `runtime_config.go` / `runtime_config_test.go` |
| `MaxConcurrentSubagents` | 进 `cfg`,从 yaml 读;`SubagentLimit` middleware 重新挂 | `config/types.go` / `yaml/config.yaml` / `middleware_chain.go` |
| prompt.go 里写死的 `5` | 全部从 `cfg.MaxConcurrentSubagents` 读 | `prompt.go` |
| `HITLTools` | 加 `yaml:"hitl_tools"` tag,从 yaml 读 | `config/types.go` / `yaml/config.yaml` |
| memory 取 agent 名 | 用当前 `agentName`,不是 `cfg.DefaultAgent` | `prompt.go` |
| `defaultHITLApproval` 跟 TUI 抢屏 | 改成包级注入点 `agent.ApprovalCallback`,TUI 启动时换成 `tea.Msg` 路由的实现 | `agent/hitl.go` / `cli/tui/hitl.go`(新) |
| `DeepAgentRuntime.SetPlanMode` | **整方法删**;plan mode 在启动时由 cfg / CLI flag 决定,session 中不再切 | `deep_runtime.go` |
| `cfg.DefaultAgent` 字段 + `cfg.Agents` map | **保留原样**;就一个 default agent,`MakeLeadAgent` 写死 `"default"` 字面量是有意 | (不动) |

---

## 1. 删 `RuntimeContext`

完全删除:

- `backend/agent/runtime_config.go`(整文件)
- `backend/agent/runtime_config_test.go`(整文件)

清理引用残骸:

- `backend/agent/subagents.go:17` — `rt *RuntimeContext` 参数已经没人读,删。signature 变成 `buildNamedSubagents(ctx, cfg, names) ([]adk.Agent, error)`。
- `backend/agent/subagents.go:30-32` — 那段 "RuntimeContext is now shared by pointer ..." 注释跟着删,过时。
- `backend/agent/subagents_test.go:13,20` — `&RuntimeContext{}` 实参删。
- `backend/agent/middleware_chain_phase3_test.go:21` — `HITLTools: []string{"shell"}` 从 rt literal 搬到 `cfg.HITLTools`(配合 §4)。

理由:RuntimeContext 现在生产代码无人读,留着是 dead indirection。AGENTS.md "**摘掉中间层**"。

---

## 2. `MaxConcurrentSubagents` 进 cfg + 重挂 `SubagentLimit`

### 2.1 Config 字段

`backend/config/types.go`,`Config` struct 加:

```go
// MaxConcurrentSubagents is the hard ceiling that SubagentLimit
// middleware enforces and the system prompt advertises to the LLM.
// Both must read this same number — drift between the prompt-stated
// limit and the runtime cap is the bug we keep relapsing into.
// Zero / negative falls back to defaultMaxConcurrentSubagents.
MaxConcurrentSubagents int `yaml:"max_concurrent_subagents"`
```

`backend/agent/prompt.go`(或一个新的 `defaults.go`)加包级 const:

```go
const defaultMaxConcurrentSubagents = 5
```

读取统一走 helper(避免 nil-coalesce 散落):

```go
// effectiveMaxSubagents returns cfg.MaxConcurrentSubagents or the default
// when unset. Single source so prompt + middleware can never drift.
func GetMaxSubagentCnt(cfg *config.Config) int {
    if cfg.MaxConcurrentSubagents <= 0 {
        return defaultMaxConcurrentSubagents
    }
    return cfg.MaxConcurrentSubagents
}
```

### 2.2 yaml + CHANGELOG

`yaml/config.yaml` 顶层加(默认值跟 const 对齐):

```yaml
max_concurrent_subagents: 5
```

`yaml/CHANGELOG.md` 加一条记录(本次结构变更)。

### 2.3 重挂 `SubagentLimit` middleware

`backend/agent/middleware_chain.go::GetChatModelMiddlewares` 签名加一个 `isSubagentEnabled bool`:

```go
func GetChatModelMiddlewares(
    ctx context.Context,
    agentName string,
    isSubagentEnabled bool,
    cfg *config.Config,
    chatModel model.BaseChatModel,
) (middlewareList []adk.ChatModelAgentMiddleware) {
    ...
    if isSubagentEnabled {
        middlewareList = append(middlewareList,
            middlewares.NewSubagentLimit(effectiveMaxSubagents(cfg)))
    }
    ...
}
```

`MakeLeadAgent` 内部调用点:`GetChatModelMiddlewares(ctx, agentName, IsSubagentEnabled, cfg, chatModel)`。

---

## 3. prompt.go 里的 `5` 全部从 cfg 读

`backend/agent/prompt.go`:

- `GetSubagentSection` / `GetSubagentReminder` / `GetSubagentThinking` 三个 helper 签名加 `n int`;6 处写死的 `5` 全部替换成 `n`。
- `GetSystemPrompt` 内部:

```go
n := effectiveMaxSubagents(cfg)
replacer := strings.NewReplacer(
    ...
    "{subagent_thinking}", GetSubagentThinking(IsSubagentEnabled, n),
    "{subagent_section}",  GetSubagentSection(IsSubagentEnabled, n),
    "{subagent_reminder}", GetSubagentReminder(IsSubagentEnabled, n),
    ...
)
```

`buildSubagentSection(5)` → `buildSubagentSection(n)`。

---

## 4. `HITLTools` 加 yaml tag

`backend/config/types.go`:

```go
HITLTools []string `yaml:"hitl_tools"`
```

`yaml/config.yaml`:

```yaml
hitl_tools: []  # 列出需要人工 approve 的工具名,如 ["shell", "execute"]
```

`yaml/CHANGELOG.md` 加一条。

`middleware_chain.go` 已经从 `cfg.HITLTools` 读,不动。

---

## 5. memory 用当前 agent 名

`backend/agent/prompt.go::GetSystemPrompt`:

```diff
- "{memory_context}", getMemoryPrompt(cfg.DefaultAgent, memorystore.NewStoreFromConfig(cfg), cfg.Memory),
+ "{memory_context}", getMemoryPrompt(agentName, memorystore.NewStoreFromConfig(cfg), cfg.Memory),
```

旧版 (`rt.AgentName`) 行为是子 agent 拿自己的 memory;refactor 中间态不小心改成 `cfg.DefaultAgent` → 子 agent 跟主 agent 共享 memory,**回退**。改回当前 `agentName`。

---

## 6. HITL approval 跟 TUI 兼容

### 6.1 现状

`backend/agent/hitl.go::defaultHITLApproval` 直接读写 `os.Stdin` / `os.Stdout`。bubbletea TUI 进 alt-screen 后独占 stdin/stdout,这套 callback 在 TUI 模式下要么 deadlock,要么撕碎屏幕。HITL 在 TUI 里实质死。

### 6.2 修法 — 包级注入点 + TUI 启动时替换

`backend/agent/hitl.go`:

```go
// ApprovalCallback is the package-wide HITL approver. The default falls
// back to a stdin y/N scanner that only works for non-TUI / batch modes.
// TUI startup MUST swap this to a tea.Msg-routed approver before the
// first MakeLeadAgent — the stdin scanner deadlocks against bubbletea
// owning stdin / stdout in alt-screen.
var ApprovalCallback = defaultStdinApproval

// defaultStdinApproval is the prior defaultHITLApproval body, renamed to
// signal "fallback only".
func defaultStdinApproval(ctx context.Context, toolName, args string) bool {
    // ... 原 defaultHITLApproval 实现搬过来
}
```

`backend/agent/middleware_chain.go`:

```diff
- middlewares.NewHITL(cfg.HITLTools, defaultHITLApproval)
+ middlewares.NewHITL(cfg.HITLTools, ApprovalCallback)
```

理由:`cfg` 是 yaml-driven 数据容器,塞函数指针进去违反 AGENTS.md "**结构体只装数据**"。每层签名加 callback 参数则违反 "**尽量少传数据**"。**包级 var + TUI init 时一次性注入**是这两条之间的最小代价折中,显式注释钉死调用契约。

### 6.3 TUI 端实现 — `backend/cli/tui/hitl.go`(新文件)

骨架(完整实现在落地阶段展开):

```go
package tui

import (
    "context"
    tea "github.com/charmbracelet/bubbletea"
    "eino-cli/backend/agent"
)

// approvalRequest is the tea.Msg variant emitted by the agent goroutine
// when a HITL-gated tool needs approval. Update routes it to the input
// pane and renders the y/N prompt.
type approvalRequest struct {
    name     string
    args     string
    decision chan bool
}

// pendingApprovals carries requests from the agent goroutine to the
// bubbletea event loop. Buffered=0 so the agent goroutine blocks until
// Update has actually picked it up — back-pressure is the safe default.
var pendingApprovals = make(chan approvalRequest)

// installTUIApproval is called once from New() before the first
// MakeLeadAgent. After this point all HITL-gated tool calls flow through
// pendingApprovals → Update → ApprovalPrompt UI → decision channel.
func installTUIApproval() {
    agent.ApprovalCallback = func(ctx context.Context, name, args string) bool {
        decision := make(chan bool, 1)
        select {
        case pendingApprovals <- approvalRequest{name, args, decision}:
        case <-ctx.Done():
            return false
        }
        select {
        case ok := <-decision:
            return ok
        case <-ctx.Done():
            return false
        }
    }
}
```

`Model` 加一个 `awaitingApproval *approvalRequest` 字段;Update 在 spinner / input 之外多一种 pane 状态(`ApprovalPrompt`),Enter 'y' / 'n' 把 decision 写回 channel 后 close。

**渲染 + 键绑定细节本 spec 不展开**(属于 TUI 形态层,跟本次的"砍 RuntimeContext"是正交问题)。本 spec 只保证:
- agent 包对外有干净注入点 (`agent.ApprovalCallback`)
- TUI 启动时调一次 `installTUIApproval()`
- 不再有 stdin scanner 跟 bubbletea 抢屏的 deadlock 风险

如果 TUI 端 `installTUIApproval` 这步漏调,fallback 仍是 `defaultStdinApproval`(行为同今天 cfg.HITLTools=空 时,即 HITL 实质不可用),不会比现状更差。

---

## 7. 删 `DeepAgentRuntime.SetPlanMode`

`backend/runtime/eino/deep_runtime.go`:

```diff
- func (r *DeepAgentRuntime) SetPlanMode(ctx context.Context, plan bool) error { ... }
```

整方法删。同时:

- `Runtime` interface(假设有)若声明了 `SetPlanMode`,同步删该方法。
- TUI 端 `/plan` slash command 调用点要么删除该 slash command,要么改为"启动时 CLI flag 决定 plan mode,session 中不可切"提示。**这一步在落地时具体定;不在本 spec 强制方案**(等 TUI 调用点确认后再定)。

理由:plan 状态原来挂在 `RuntimeContext.IsPlanMode`,RuntimeContext 删了之后,这个状态没地方存。每次 `SetPlanMode` 都会强制重建 `r.runner`,把 in-flight 状态清空 —— 没价值,且 user 明确说不要。

---

## 8. 测试机械同步

按 `go test ./...` 当前 build 错误清单一一改:

| 文件:行 | 修法 |
|---|---|
| `backend/runtime/eino/runtime_test.go:28,41,66` | `map[string]config.AgentConfig{}` → `map[string]*config.AgentConfig{}` |
| `backend/runtime/eino/runtime_test.go:129` | `r.rt.IsPlanMode` 用法删除(整测试可能要重写或删,跟 §7 SetPlanMode 一起处理) |
| `backend/agent/agent_loader_test.go:11,30,44,55,66` | 同 map 类型修复 |
| `backend/agent/memory_e2e_test.go:57,124` | `GetChatModelMiddlewares(ctx, agentName, isSubagentEnabled, cfg, chatModel)` 新签名 |
| `backend/agent/middleware_chain_phase3_test.go:21,37` | 同上 + `HITLTools` 从 rt literal 搬到 cfg literal |
| `backend/agent/subagents_test.go:13,20` | 删 `&RuntimeContext{}` 实参 |

---

## 9. 渐进次序(每步一 commit)

1. **config 加字段 + yaml 同步** — `MaxConcurrentSubagents` / `HITLTools` 加 yaml tag,`yaml/config.yaml` 新增两节,`yaml/CHANGELOG.md` 加两条。
2. **prompt.go 写死 5 → cfg 读取** — 加 `effectiveMaxSubagents` helper + `defaultMaxConcurrentSubagents` const,改 6 处写死。
3. **重挂 `SubagentLimit` middleware** — `GetChatModelMiddlewares` 签名加 `isSubagentEnabled bool`,`MakeLeadAgent` 调用点同步;测试同步签名。
4. **memory 用 `agentName`** — `prompt.go` 一行 diff。
5. **HITL approval 包级注入点** — `hitl.go` 加 `ApprovalCallback` var + 改名 `defaultStdinApproval`;`middleware_chain.go` 引用切换。**TUI 端 `installTUIApproval()` 实现单独一 commit**(可放在本 spec 之外的 follow-up,因为牵扯 TUI 模块状态机)。
6. **删 `RuntimeContext`** — `runtime_config.go` + `runtime_config_test.go` 整体删,清 `subagents.go` 死参数 / 注释,清 `subagents_test.go` 实参。`go build ./...` 应仍通过。
7. **删 `SetPlanMode`** — `deep_runtime.go` 删方法,TUI 调用点同步处理(具体方式落地时定)。
8. **修剩余测试** — 跟改完 `go test ./...` 绿。

每步 diff 一句话能讲清,符合 AGENTS.md "**每个 commit 的 diff 要能用一句话说清**"。

---

## 10. 不做(non-goals)

- **不**重写 `HITL` middleware 自身(`middlewares/hitl.go`)。它是好的,只是入口断了。
- **不**动 `SubagentLimit` middleware 内部逻辑(`middlewares/subagent_limit.go`)。重挂位置就行。
- **不**做"多 agent 切换"功能(`Runtime.SetAgentName` 类的 setter)。`DefaultAgent` 字段保留但仅作为常量,`Agents` map 结构保留但只装一条 default —— 留给未来如果真的需要多 agent profile,届时再扩,不为 YAGNI 做事。
- **不**重做 TUI 的 `ApprovalPrompt` 视觉形态。本 spec 只给出 `installTUIApproval()` 注入骨架,完整 UX 走 follow-up spec。
- **不**给 `RuntimeContext` 写"墓志铭" doc。删就删,`AGENTS.md` 风格里没有 deprecation graveyard。
