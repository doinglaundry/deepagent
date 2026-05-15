# Step 1: Quick Wins — Plan mode 入口 / Token footer / Shimmer / AGENTS.md

> 背景：`specs/20260514-feature-comparison/design.md` §3 推荐的"下一步第 1
> 步"。本文档把 4 个已经写完但没开关的能力点亮，整体打包成一个 PR，按
> commit 粒度可拆 5 个 commit（A4 拆 agent 层 / TUI 层 = 2 个，
> A1 / A5 / A6 各 1 个）。

## 总览

- **痛点**：plan mode、token footer 都是"中间件已经实现、调用方没接"；
  loading 文案缺 shimmer；AGENTS.md 没注入到 system prompt。用户感知
  最强、改动最小。
- **改动范围**：新增 `PlanReminder` chat-model 中间件（替代死 build-time
  `AdditionalInstruction` 路径）、`Trace` 加 1 个 phase、`DeepAgentRuntime`
  加 `atomic.Bool` plan-mode flag、`prompt.go` 加 `loadAgentsMDPrompt`
  helper + `{agents_md}` 占位符把 AGENTS.md 静态拼入 system prompt、
  `tui/verbs.go` 加 shimmer 渲染、`tui/commands.go` 加 `/plan` 注册。
  `TokenUsage` / `Title` 中间件本身不动，已经存在并能跑。**不动 yaml
  shape**——plan mode 是 session 内 toggle，不持久化。

---

## A1 · Plan mode 入口

### 目标

`backend/agent/middlewares/todo.go::TodoInstruction` 的"用 write_todos
做计划"那段提醒文本，今天通过 `MakeLeadAgent(IsPlanMode=true)` →
`GetAgentMiddleWares` → `NewTodo()` 的 `AdditionalInstruction` 被 eino
**一次性拼进** agent 的固定 instruction（见
`adk/chatmodel.go::prepareExecContext` 第 743 行），**构造完成后无法
改写**。唯一调用方 `backend/runtime/eino/deep_runtime.go:47` 又写死了
`false`：

```47:50:backend/runtime/eino/deep_runtime.go
	leadAgent, trace, err := agent.MakeLeadAgent(ctx, "default", false, true, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("build lead agent: %w", err)
	}
```

`write_todos` 工具本身**与 plan mode 无关**——`deep.Config.WithoutWriteTodos`
为 `false`，工具永远挂着；plan mode 只决定模型有没有看到那段 preamble。

预期效果：

- 启动默认 plan mode = off（零值），不读 yaml。
- TUI 下 `/plan` / `/plan on` / `/plan off` / `/plan toggle` 切换。
- 切换 = 一次 `atomic.Bool.Store`——不重建 agent，不动 `r.runner` /
  `r.trace`，无锁竞争。
- 切换后下一轮 model call 立即生效——`ChatModelAgentMiddleware` 在
  `BeforeModelRewriteState` 钩子里读 flag、把 `TodoInstruction` 拼到
  首条 system message 末尾。模型看到的 prompt 字符串与现状（build-time
  `AdditionalInstruction` 路径）逐字符等价。

### 实现代码

**1. `backend/runtime/eino/runtime.go::Runtime` 接口加方法**——返回值起
名当文档用，避免裸 `bool` 在调用点歧义（"成功?" / "新状态?" / "是否
真切了?"）：

```go
type Runtime interface {
    // ... 已有方法 ...
    SetPlanMode(ctx context.Context, on bool) (newState bool, err error)
}
```

`newState` 让 TUI 渲染时直接拿到切换后的状态，不必再 query 一次。impl
端 `*DeepAgentRuntime.SetPlanMode` 不需要 named returns——body 三行 +
一句 `return on, nil`，名字加不出信息。

**2. `backend/runtime/eino/deep_runtime.go` 改两处**：

- `DeepAgentRuntime` 加字段 `planMode atomic.Bool`（零值 `false`，
  **不读 config**），并把 `r.planMode.Load` 闭包传给 `buildLeadRunner`：

  ```go
  // === 已有 ===
  type DeepAgentRuntime struct {
      cfg                 *config.Config
      modelName           string
      runner              *adk.Runner
      mu                  sync.Mutex
      pendingCheckpointID string
      history             []*schema.Message
      maxHistoryTurns     int
      trace               *middlewares.Trace
      // === 新增 ===
      planMode            atomic.Bool
  }

  func NewDeepAgentRuntime(ctx context.Context, cfg *config.Config) (Runtime, error) {
      r := &DeepAgentRuntime{
          cfg:             cfg,
          modelName:       cfg.DefaultModel,
          maxHistoryTurns: 20,
      }
      runner, trace, err := buildLeadRunner(ctx, cfg, r.planMode.Load)
      if err != nil {
          return nil, err
      }
      r.runner = runner
      r.trace = trace
      return r, nil
  }
  ```

  `buildLeadRunner` / `ReloadSoul` 同步加 `getPlanMode func() bool`
  形参，透传给 `agent.MakeLeadAgent`。

- 新增方法 `SetPlanMode`——纯 atomic store，不动 runner / trace / mu：

  ```go
  func (r *DeepAgentRuntime) SetPlanMode(_ context.Context, on bool) (bool, error) {
      r.planMode.Store(on)
      return on, nil
  }
  ```

**3. `backend/agent/middlewares/plan_reminder.go` 新建**——抄
`todo_reminder.go` 的壳，把 `TodoInstruction` 拼到首条 system message：

```go
package middlewares

import (
    "context"
    "strings"

    "github.com/cloudwego/eino/adk"
    "github.com/cloudwego/eino/schema"
)

// planModeTag is the idempotency anchor: TodoInstruction already wraps
// itself in <plan_mode>...</plan_mode>, so a retry / interrupt-resume
// pass that sees this tag in msgs[0] knows the append already happened.
const planModeTag = "<plan_mode>"

// PlanReminder appends TodoInstruction onto the existing system message
// when getOn() returns true. String content is identical to the
// build-time AdditionalInstruction path that this replaces, so the model
// observes no behavioural difference.
type PlanReminder struct {
    *adk.BaseChatModelAgentMiddleware
    getOn func() bool
}

func NewPlanReminder(getOn func() bool) *PlanReminder {
    return &PlanReminder{
        BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
        getOn: getOn,
    }
}

func (m *PlanReminder) BeforeModelRewriteState(
    ctx context.Context,
    state *adk.ChatModelAgentState,
    _ *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
    if state == nil || !m.getOn() {
        return ctx, state, nil
    }
    state.Messages = appendPlanInstruction(state.Messages)
    return ctx, state, nil
}

// appendPlanInstruction targets msgs[0] when it's the agent's system
// instruction; idempotent via planModeTag. Returns msgs unchanged when
// the tag is already present (replay / interrupt-resume).
func appendPlanInstruction(msgs []*schema.Message) []*schema.Message {
    if len(msgs) == 0 || msgs[0] == nil || msgs[0].Role != schema.System {
        return msgs
    }
    if strings.Contains(msgs[0].Content, planModeTag) {
        return msgs
    }
    cloned := *msgs[0]
    cloned.Content += TodoInstruction
    out := make([]*schema.Message, len(msgs))
    out[0] = &cloned
    copy(out[1:], msgs[1:])
    return out
}
```

**4. `backend/agent/middleware_chain.go` 改两处**：

- `GetChatModelMiddlewares` 签名加 `getPlanMode func() bool`，挂
  `NewPlanReminder(getPlanMode)` **在 `NewTodoReminder` 之前**——
  `PlanReminder` 改 `msgs[0]`，`TodoReminder` 在 `msgs[0]` 前面 prepend
  新 system；如果反过来，`PlanReminder` 会把 plan 文本拼到 todo
  reminder 上，目标错位：

  ```go
  // PlanReminder runs BEFORE TodoReminder so it targets the agent's
  // own system instruction (msgs[0]). TodoReminder later prepends a
  // separate system message; if PlanReminder ran second, it would
  // mistakenly append plan text onto the todo reminder.
  middlewareList = append(middlewareList, middlewares.NewPlanReminder(getPlanMode))
  middlewareList = append(middlewareList, middlewares.NewTodoReminder())
  ```

- **删除 `GetAgentMiddleWares` 整个函数**——`adk.AgentMiddleware` 路径
  在本仓库只有这一个用例（plan mode），已被 `PlanReminder` 替换。
  `lead_agent.go::deepCfg.Middlewares` 同步删除。AGENTS.md "8+ 字段才
  考虑结构体" 的对偶："只剩 1 个 trivial 用例的辅助函数 → 删"。

**5. `backend/agent/lead_agent.go::MakeLeadAgent` 签名变化**：

```go
func MakeLeadAgent(
    ctx context.Context,
    agentName string,
    // === 已有 IsPlanMode bool 删除 ===
    IsSubagentEnabled bool,
    getPlanMode func() bool, // === 新增 ===
    cfg *config.Config,
) (adk.ResumableAgent, *middlewares.Trace, error) {
    // ...
    handlers := GetChatModelMiddlewares(ctx, agentName, IsSubagentEnabled, getPlanMode, cfg, chatModel)
    // ...
    deepCfg := &deep.Config{
        // ...
        // === 已有 Middlewares: GetAgentMiddleWares(IsPlanMode) 删除 ===
        Handlers: handlers,
        // ...
    }
    // ...
}
```

**6. `backend/cli/tui/commands.go::builtinCommands` 追加一行**（保持
alphabetical 顺序）：

```go
{Name: "plan", Args: "[on|off|toggle]", Desc: "toggle plan mode (write_todos preamble)", Type: "builtin"},
```

**7. `backend/cli/tui/update.go::handleBuiltin` switch 加 `case "plan":`**，
走新 helper `handlePlanCmd`（命令风格抄 `handleDebugCmd` /
`handleTodosCmd`）：

```go
func (m *Model) handlePlanCmd(text string) tea.Cmd {
    if m.streaming || m.bootstrapLoading {
        m.pushMessage("system", "finish or cancel the current response before /plan")
        return nil
    }
    arg := strings.TrimSpace(strings.TrimPrefix(text, "/plan"))
    on, ok := parsePlanArg(arg, m.planMode)
    if !ok {
        m.pushMessage("system", "usage: /plan [on|off|toggle]")
        return nil
    }
    rt := m.rt
    return func() tea.Msg {
        newState, err := rt.SetPlanMode(context.Background(), on)
        return planSetMsg{on: newState, err: err}
    }
}

// parsePlanArg returns (newState, true) for empty/toggle/on/off; (_, false) otherwise.
func parsePlanArg(arg string, current bool) (bool, bool) {
    switch arg {
    case "", "toggle":
        return !current, true
    case "on":
        return true, true
    case "off":
        return false, true
    }
    return false, false
}
```

**8. `backend/cli/tui/model.go::Model` 加字段** `planMode bool`（零值
`false`，**不读 config**）；`Update` 收到 `planSetMsg` 后回填
`m.planMode` 并 `pushMessage("system", "plan = on/off")`。

### 取舍

**设计选择**：

- **消息注入而不是 build-time `AdditionalInstruction`**：eino 的
  `instruction` 字段在 `deep.New(ctx, deepCfg)` 时凝固，没有 setter。
  走 build-time 路径每次切换都要 `buildChatModel` + `deep.New` 重建，
  还要复用 `ReloadSoul` 的 `r.mu` 把 `r.runner` / `r.trace` 一并换掉。
  消息注入路径切换只是 `atomic.Bool.Store`——AGENTS.md "尽量少压调用
  栈"，能在叶节点解决就别向上漫延到整个 agent 重建。
- **拼到首条 system message 末尾，而不是 prepend 一条新 system 消息**：
  字符串拼接结果与现状逐字符相同，模型注意力一致；多塞一条 system
  在部分模型上会被弱化处理。`TodoReminder` 走 prepend 是因为它注入
  的内容跟 agent instruction 是不同语义（运行时提醒 vs 行为契约）；
  plan mode 是行为契约的一部分，归到 instruction 自己里更对。
- **`atomic.Bool` 而不是 mutex**：`SetPlanMode` 跟 `BeforeModelRewriteState`
  是一写多读、bool 一个值——`atomic.Bool` 是这种 pattern 的标配，
  比锁轻、比 channel 直接。AGENTS.md "结构体只装数据 / 不藏 callback
  字段"——`planMode` 就是一个原子 bool。
- **`MakeLeadAgent` 删 `IsPlanMode bool` 加 `getPlanMode func() bool`**：
  build-time 标记（"这次 agent 永远 plan mode"）→ 运行时 reader（
  "model call 时去问当前 plan mode"）。AGENTS.md "尽量少传数据"——
  不再向 agent 灌一个 stale 标记。
- **删 `GetAgentMiddleWares` 整个函数**：`adk.AgentMiddleware` 路径
  在本仓库只有 plan mode 一个用例，被 `PlanReminder` 完全替代。AGENTS.md
  "结构体 7 个字段只接 2 个 → 剩下 5 个删掉" 的对偶——只服务一个
  trivial 用例的辅助函数 → 删。
- **`<plan_mode>` tag 复用做幂等**：`TodoInstruction` 常量本来就以
  `<plan_mode>` 包裹，天然适合做注入幂等检查。`TodoReminder` 也是
  这个套路（`<system_reminder type="todo">`）。
- **`parsePlanArg` 写成 switch 而不是嵌套 if/else**：AGENTS.md "二选
  一映射 → switch / lookup 表"。

**副作用 / 风险**：

- **`MakeLeadAgent` 签名 break**：删 `IsPlanMode bool` 第 3 个参数、
  加 `getPlanMode func() bool` 第 4 个参数。`backend/agent/lead_agent.go`
  唯一调用点 `deep_runtime.go::buildLeadRunner` 同步改。无外部公共 API。
- **`GetAgentMiddleWares` 删除 →
  `middleware_chain_phase3_test.go:65-71, 94-96` 两块断言要删**：
  `TestGetChatModelMiddlewares_..._PlanMode` 那条 `len(GetAgentMiddleWares(true)) == 1`
  和 `TestGetChatModelMiddlewares_NoGatesEmittedWhenDisabled` 末尾那条
  `len(GetAgentMiddleWares(false)) == 0`，都因函数消失而需要删除并替
  换为 `PlanReminder` 是否在 chain 中的断言。
- **`backend/agent/middlewares/todo.go` 里 `NewTodo()` 函数变 unused**：
  `TodoInstruction` 常量保留（`PlanReminder` 复用），`NewTodo()` 同
  commit 一并删。文件可能只剩一个常量——按 AGENTS.md "矫枉过正预警"
  反向：单常量文件可以保留或并入 `plan_reminder.go`。本步保留 `todo.go`，
  里面只剩 `TodoInstruction` 常量；并入是后续清理的事。
- **接口加方法 → 测试 stub 要补**：`hitl_e2e_test.go::stubRuntime` /
  `reload_test.go::reloadRuntime` 等 4 个 stub 需要加
  `SetPlanMode(...) (bool, error) { return false, nil }`。
- **`Trace.ResetTurn()` 不再被附带调用**：旧设计 plan mode 切换会因
  rebuild 顺带拿到新 trace 调一次 `ResetTurn()`；新设计不动 trace，
  turn 计数延续。这其实更对——plan mode 不是会话边界，没理由 reset turn。
- **零额外开销**：每个 model call 多一次 `atomic.Bool.Load`（纳秒级），
  off 时直接 return，不 alloc；on 时多一次 `msgs[0]` 浅拷贝（一个
  `schema.Message` struct）。

**回滚**：

- 无 yaml flag——回滚靠代码：删 `backend/agent/middlewares/plan_reminder.go`、
  `middleware_chain.go` 拿掉 `NewPlanReminder` 一行 + 删 `getPlanMode`
  形参、`lead_agent.go` 签名复原 + `Middlewares: GetAgentMiddleWares(...)`
  恢复（同时恢复 `todo.go::NewTodo()`、`middleware_chain.go::GetAgentMiddleWares`）、
  `deep_runtime.go` 删 `planMode` 字段 + `SetPlanMode` 方法、`Runtime`
  接口拿掉 `SetPlanMode` + 4 个 stub 同步删、TUI 拿掉 `/plan` 注册 +
  `handlePlanCmd` + `parsePlanArg` + `planMode` 字段——共 7 处局部修改。

---

## A4 · Token count footer

### 目标

`backend/agent/middlewares/token_usage.go::TokenUsage` 已在每个 model turn
累加 `prompt/completion/total tokens`，提供 `Snapshot() TokenUsageStats`，
但 TUI 没有消费方——`renderFooter` 只打 `modelName · hint`。本步把累计
token 接到 footer。

预期效果：

- footer 左侧从 `kimi` 变成 `kimi · 3.4k tokens`；右侧 hint 段不变。
- token 总数为 0 时不显示（空对话不加噪声）。
- 每个 model turn 结束后实时刷新。
- `cfg.TokenUsage.Enabled = false` 时 footer 退化为现状。

### 实现代码

`Trace` 是 TUI 与 agent 之间唯一的事件通道（已有 Before / After / Todos
三个 phase），TUI 端 `handleTraceEvent` 已经在监听。**复用这条流**——加
第 4 个 phase，不开第 2 条事件总线。

**1. `backend/agent/middlewares/trace.go` 加 phase + 事件字段**：

```go
const (
    TracePhaseBefore = iota + 1
    TracePhaseAfter
    TracePhaseTodos
    TracePhaseTokens
)

type TraceEvent struct {
    AgentName string
    Phase     int
    Turn      int
    Messages  []*schema.Message
    Todos     []deep.TODO       // only when Phase == TracePhaseTodos
    Tokens    *TokenUsageStats  // only when Phase == TracePhaseTokens
}
```

**2. `Trace` 加可选 snapshot 钩子**（字段，不是新结构体——按 AGENTS.md
"结构体只装数据"，能加字段就别加并行容器）：

```go
type Trace struct {
    *adk.BaseChatModelAgentMiddleware
    agentName string
    turn      atomic.Int64

    // TokenSnapshot emits a TracePhaseTokens event after every model turn
    // when set. Wired from GetChatModelMiddlewares iff cfg.TokenUsage.Enabled.
    TokenSnapshot func() TokenUsageStats
}
```

**3. `Trace.AfterModelRewriteState` 末尾 piggy-back token 段**——紧跟现
有 todos 段后面（同一个 After 钩子，复用 ctx lookup + consumer 检查）：

```go
// === 已有 ===
if raw, ok := adk.GetSessionValue(ctx, deep.SessionKeyTodos); ok {
    todos, _ := raw.([]deep.TODO)
    if len(todos) > 0 {
        consumer.Send(TraceEvent{ /* Todos */ })
    }
}
// === 新增 ===
if t.TokenSnapshot != nil {
    stats := t.TokenSnapshot()
    consumer.Send(TraceEvent{
        AgentName: t.agentName,
        Phase:     TracePhaseTokens,
        Turn:      int(t.turn.Load()),
        Tokens:    &stats,
    })
}
return ctx, state, nil
```

**4. `backend/agent/middleware_chain.go::GetChatModelMiddlewares` 把
TokenUsage 指针留住、塞给 Trace**：

```go
var tokenUsage *middlewares.TokenUsage
if cfg.TokenUsage.Enabled {
    tokenUsage = middlewares.NewTokenUsage()
    middlewareList = append(middlewareList, tokenUsage)
}
// ... 其它 middleware（链顺序不动） ...
trace := middlewares.NewTrace(agentName)
if tokenUsage != nil {
    trace.TokenSnapshot = tokenUsage.Snapshot
}
middlewareList = append(middlewareList, trace)
middlewareList = append(middlewareList, middlewares.NewClarification())
```

原来 `NewTrace(agentName)` 直接 append；这里展开成局部变量再 append 仅
为填 `TokenSnapshot` 字段，未引入新抽象。

**5. `backend/cli/tui/model.go::Model` 加 1 个字段**：

```go
tokenTotal int64
```

**6. `backend/cli/tui/update.go::handleTraceEvent` switch 加 case**：

```go
case middlewares.TracePhaseTokens:
    if ev.Tokens != nil {
        m.tokenTotal = ev.Tokens.TotalTokens
    }
    return m, nil
```

不调 `recomputeLayout`——footer 高度恒为 1 行，token 数量变化只重渲染
字符。

**7. `backend/cli/tui/view.go::renderFooter` 把 token 段塞进 `left`**：

```go
func (m *Model) renderFooter() string {
    left := footerStyle.Render(m.modelName)
    if m.tokenTotal > 0 {
        left += footerStyle.Render(" · " + formatTokenCount(m.tokenTotal))
    }
    // ... hint / gap / 其余不变 ...
}

// formatTokenCount: >=1000 → "3.4k tokens"; <1000 → "<n> tokens".
func formatTokenCount(n int64) string {
    if n >= 1000 {
        return fmt.Sprintf("%.1fk tokens", float64(n)/1000)
    }
    return fmt.Sprintf("%d tokens", n)
}
```

### 取舍

**设计选择**：

- **piggy-back 在 Trace，不开第 2 条事件流**：TUI 已经在监听
  `TraceConsumer`，再开一个就是两套订阅。AGENTS.md "尽量少压调用栈" /
  "尽量少传数据"——能复用就复用。
- **`TokenSnapshot` 是字段不是新结构体**：替代方案是引入
  `TokenAwareTrace`，把 `Trace` 包一层。AGENTS.md "矫枉过正预警"——
  8+ 字段才考虑结构体；这里就一个 `func() TokenUsageStats`，挂上去
  刚好。
- **`Tokens *TokenUsageStats` 而不是值**：`TraceEvent` 是 by-value 通过
  channel 传，全 phase 共用一个 struct；指针 + 单 phase 可见的 nil 语义
  跟现有 `Todos []deep.TODO`（empty slice for non-Todos phase）对齐——
  trace.go 头部注释已经写明"同一个结构体，不同 phase 填不同字段"。
- **footer 用 `left` 拼接而不是新增 `center`**：`renderFooter` 现在
  `left + gap + right` 两段；硬塞中段就要重算 gap 三次。token 跟
  modelName 同属于"当前 session 元数据"，自然归 left。

**副作用 / 风险**：

- 当 `cfg.TokenUsage.Enabled = true`（默认），每个 turn 多 1 次
  `consumer.Send` + 1 次 `mu.Lock + struct copy`（4 个 int64）。可忽略。
- `TraceEvent` 加字段是 backward-compatible 的——已有 phase 不读
  `Tokens`，不读就是 zero value。
- `FindTrace` 类型断言不变（`*Trace` 仍是同一类型）。
- `esc_footer_test.go` 已有的 footer 断言（`/help` / `ctrl-c` /
  `esc to interrupt`）不受影响——这些都在 right hint 段，token 段只挂
  left。

**回滚**：

- 把 yaml `token_usage.enabled` 关掉，`tokenUsage = nil` →
  `trace.TokenSnapshot` 不被填 → phase 不发 → footer 退化为现状。
- 真要彻底回滚：删 `TracePhaseTokens` 常量、`TraceEvent.Tokens` 字段、
  `Trace.TokenSnapshot` 字段、`handleTraceEvent` 那一个 case、
  `renderFooter` 三行块。各处都是局部修改。

---

## A5 · Loading 文案 shimmer

### 目标

`backend/cli/tui/verbs.go` 已经是 15 条 `(present, past)` 池，`pickVerb`
随机返回一对（更正 design.md 中"单一 verb"的描述）。`renderStreamPanel`
现状是静态文字：

```174:182:backend/cli/tui/view.go
func (m *Model) renderStreamPanel() string {
	if m.streaming || m.bootstrapLoading {
		secs := int(m.elapsed.Seconds())
		return fmt.Sprintf("%s %s %s",
			thinkingMarkerStyle.Render("✶"),
			thinkingPresentStyle.Render(m.verbPresent+"…"),
			dimStyle.Render(fmt.Sprintf("(%ds · thinking)", secs)),
		)
	}
```

缺的是 **shimmer 效果**——helixent 在 verb 文字上滚一个高亮窗口
（`SHIMMER_WIDTH=3`），靠 120ms tick 推进。本步把 shimmer 加上。

预期效果：

- 流式中，`Moonwalking…` 上有一个 3-char 宽亮窗左→右循环。
- 不流式时（idle / error）不动。
- 不破坏已有的 verb 池（present→past 对齐契约 scrollback 还在用）。

### 实现代码

**1. `backend/cli/tui/model.go::Model` 加 1 个字段**：

```go
shimmerOffset int
```

**2. `backend/cli/tui/update.go::spinner.TickMsg` 分支推进 offset**，复
用现有 100ms tick（不另起 `tea.Tick`——AGENTS.md "尽量少压调用栈"）：

```go
case spinner.TickMsg:
    if !m.streaming && !m.bootstrapLoading {
        return m, nil
    }
    var cmd tea.Cmd
    m.spin, cmd = m.spin.Update(msg)
    m.elapsed = time.Since(m.streamStart).Round(time.Second)
    m.shimmerOffset++  // === 新增 ===
    return m, cmd
```

**3. `backend/cli/tui/verbs.go` 加纯顶层函数 `renderShimmer`**（按
AGENTS.md "行为住在普通顶层函数里"——shimmer 跟 model 状态无关）：

```go
const shimmerWidth = 3

// renderShimmer overlays a 3-char highlight window onto text. offset
// advances per tick; the visible window wraps modulo (len(text)+2*shimmerWidth)
// so it scans in from the left and out to the right.
func renderShimmer(text string, offset int, base, bright lipgloss.Style) string {
    if text == "" {
        return ""
    }
    cycle := len(text) + shimmerWidth*2
    pos := offset % cycle
    start := pos - shimmerWidth
    end := pos + shimmerWidth
    if start >= len(text) || end <= 0 {
        return base.Render(text)
    }
    lo := max(0, start)
    hi := min(len(text), end)
    return base.Render(text[:lo]) + bright.Render(text[lo:hi]) + base.Render(text[hi:])
}
```

**4. `backend/cli/tui/styles.go` 加亮色 style**：

```go
thinkingShimmerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("13")) // bright magenta
```

**5. `backend/cli/tui/view.go::renderStreamPanel` 用 shimmer 渲染 verb**：

```go
verb := renderShimmer(m.verbPresent+"…", m.shimmerOffset,
    thinkingPresentStyle, thinkingShimmerStyle)
return fmt.Sprintf("%s %s %s",
    thinkingMarkerStyle.Render("✶"),
    verb,
    dimStyle.Render(fmt.Sprintf("(%ds · thinking)", secs)),
)
```

### 取舍

**设计选择**：

- **复用 `spinner.TickMsg` 而不是 `tea.Tick`**：100ms 频率刚好（120ms
  也行），多开一条 tick 流就要管两套生命周期。`TickMsg` 已经在
  streaming/bootstrap 才推进，shimmer 自然继承同样 lifecycle。
- **`renderShimmer` 顶层函数不挂 receiver**：函数体只用 `text` /
  `offset` / 两个 style 参数，不读 model 状态——按 AGENTS.md "行为住
  普通顶层函数"判定。
- **byte 切片而不是 rune 切片**：verb 池全是 ASCII（`Moonwalking` /
  `Brainstorming` / 等），byte 切片直接对应字符位置。AGENTS.md "矫枉过
  正预警"——CJK verb 是不存在的需求。未来真加，把切片换 rune 即可，
  函数 shape 不变。
- **不做多帧 spinner**：helixent 那套 `["·", "✢", "✳", "✶", "✻", "✽"]`
  动画 `bubbles/spinner` 已经自带，没必要重写。

**副作用 / 风险**：

- 每 100ms 多一次轻量字符串切片 + 3 段 style render。可忽略。
- lipgloss 拼接 3 段会有 3 个 ANSI reset 序列；这是 view.go 其他位置已
  有的 pattern（见 `renderTodoLine`），终端宽度计算 / 视觉断行没问题。
- `shimmerOffset` 是 `int`，每 100ms +1，跑满 64-bit 要 5800 万年——
  不需要 wrap-around 处理。
- `renderStreamPanel` 测试（`thinking_indicator_test.go`）的现有断言仍
  能通过——shimmer 只切样式，不丢字符（`text[:lo] + text[lo:hi] +
  text[hi:]` 拼回原文）。

**回滚**：

- 删 `renderShimmer` 调用，退回单一 `thinkingPresentStyle.Render`。
- `renderShimmer` / `thinkingShimmerStyle` / `shimmerOffset` 字段保留
  也无副作用（unused，linter 会提示）。

---

## A6 · AGENTS.md 自动注入

### 目标

`AGENTS.md` 是 Cursor / Codex 通用约定——项目根工程约束，**面向 agent
本身**告诉它怎么写代码——跟 `yaml/soul.md`（agent 人格）正交。

本仓库 `backend/agent/prompt.go::GetSystemPrompt` 已经有完整的占位符
模板，`{soul}` 被 `loadSoulPrompt(cfg)` 读 `<root>/yaml/soul.md` 后包
进 `<soul>...</soul>` 拼到 system prompt 里：

```535:548:backend/agent/prompt.go
func loadSoulPrompt(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(cfg.RootDir, "yaml", "soul.md"))
	if err != nil {
		return ""
	}
	body := strings.TrimSpace(string(data))
	if body == "" {
		return ""
	}
	return "<soul>\n" + body + "\n</soul>"
}
```

AGENTS.md 跟 soul.md 是**同家族的项目级元数据**，生命周期、文件 IO
pattern、注入时机完全一致——理应同模板。

预期效果：

- system prompt 永久包含 `<workspace_conventions>...</workspace_conventions>`
  段，AGENTS.md 全文嵌入其中。每个 model call 都能看到。
- 仓库没有 `AGENTS.md` / 文件为空 → 占位符空字符串，模板对应位置不留
  多余空行（跟 `{soul}` 同行为）。
- 用户改 AGENTS.md 文件 → `/reload` (`ReloadSoul`) 重建 lead agent 时
  重读 disk；同 session 内不重读（跟 `{soul}` 同语义）。
- KV cache 命中：system prompt 是最稳定的 cache 前缀，OpenAI /
  Anthropic / Kimi 都对这段做缓存优化，长会话每轮成本接近 0。

### 实现代码

只动 [backend/agent/prompt.go](backend/agent/prompt.go) 一个文件。**不
动** [backend/runtime/eino/deep_runtime.go](backend/runtime/eino/deep_runtime.go)
——AGENTS.md 跟 history 无关，`ExecuteStream` 不需要任何改动。

**1. 加顶层 helper**（紧靠 `loadSoulPrompt` 后面，按 AGENTS.md "行为
住普通顶层函数"，跟 soul 同 shape）：

```go
// loadAgentsMDPrompt mirrors loadSoulPrompt — same shape, different file.
// AGENTS.md is the Cursor/Codex project-conventions doc; semantically
// project metadata, lifecycle identical to soul.md.
func loadAgentsMDPrompt(cfg *config.Config) string {
    if cfg == nil {
        return ""
    }
    data, err := os.ReadFile(filepath.Join(cfg.RootDir, "AGENTS.md"))
    if err != nil {
        return ""
    }
    body := strings.TrimSpace(string(data))
    if body == "" {
        return ""
    }
    return "<workspace_conventions>\n" + body + "\n</workspace_conventions>"
}
```

`os.ReadFile` 把 ENOENT / 权限错误吞咽成 `""`——AGENTS.md 是可选约
定，语义跟 `loadSoulPrompt` 完全对齐。

**2. `GetSystemPrompt` replacer 加一行**（在 `{soul}` 旁边，紧邻同家
族）：

```go
replacer := strings.NewReplacer(
    "{agent_name}", agentName,
    "{soul}", loadSoulPrompt(cfg),
    "{agents_md}", loadAgentsMDPrompt(cfg), // === 新增 ===
    "{memory_context}", getMemoryPrompt(...),
    // ... 其余不变 ...
)
```

**3. `systemPromptTemplateRaw` 模板加一行**（`{soul}` 下面紧贴一行，
保持"项目元数据先于业务说明"的视觉分组）：

```
{soul}
{agents_md}
{memory_context}
```

文件 IO 副作用：`GetSystemPrompt` 现在每次调用多读一个文件
（`<root>/AGENTS.md`）。仅在 `MakeLeadAgent` / `ReloadSoul` 路径触
发——和 `loadSoulPrompt` 同频率，每个 session 一次或几次。

### 取舍

**设计选择**：

- **走 system prompt 而不是 history user 消息**：本仓库已经有
  `{soul}` 这套模板基础设施，AGENTS.md 跟 soul 在语义、生命周期、文
  件 IO 上完全对称——AGENTS.md "矫枉过正预警"反向：现成 pattern 直
  接抄，不为同类问题造第二条路径。helixent 走 history 注入是因为它
  没有 prompt template，本仓库走那条路反而是劣化。
- **`<workspace_conventions>` 块名**：XML 风格跟 `<soul>` / `<root>` /
  `<skills_section>` 对齐；`workspace` 比 `agents_md` 更语义化（描述
  内容性质，不是文件名）。
- **删 `agentsMDPromptPrefix` 那条人类可读 marker**：原方案抄 helixent
  的 `> The AGENTS.md file has been automatically loaded...`，是因为
  user role 没有自然分隔；走 system prompt 后 `<workspace_conventions>`
  XML 块本身就是 marker，模型识别足够。
- **不挂 receiver**：AGENTS.md 跟 runtime 状态无关，按 AGENTS.md "行
  为住普通顶层函数"。跟 `loadSoulPrompt` 风格一致。
- **占位符放 `{soul}` 紧邻**：项目元数据视觉聚团，比插在
  `{memory_context}` 后或 `{root_dir}` 旁更对应"加载顺序"。

**副作用 / 风险**：

- **每轮 system prompt +5KB**——但 KV cache prefix 命中（OpenAI /
  Anthropic / Kimi cache hit token 按 1/10 计费），长会话比"history
  user 消息 + 20 轮后 trim"还便宜。短会话（<5 轮）每轮 +5KB 计费
  token，约 +1250 tokens；本仓库 default model `kimi-k2` 输入价
  ¥0.0001/token，单轮 +¥0.125，可忽略。
- **`prompt_test.go` 现有快照 / 子串断言**：
  `TestGetSystemPrompt_SkillsAndDeferredSectionsRendered` /
  `TestGetSystemPrompt_EmptyMemorySkipsBlock` 不依赖 AGENTS.md 段
  存在与否——cfg.RootDir 在测试里通常指向 t.TempDir() 或固定测试
  目录，没 AGENTS.md → `{agents_md}` 替换为 `""`，模板渲染等同现
  状。**没有断言会因这次改动挂**。
- **同 session 内 AGENTS.md 修改不立即生效**：要走 `/reload`。这跟
  `{soul}` 现状一致（用户改 `yaml/soul.md` 也要 `/reload`）——一
  致即正确。
- **大 AGENTS.md**：本仓库 ~5KB；用户仓库若 >50KB 永久挂 system
  prompt 每轮成本可观。**不在本步限流**——用户写大文档视为有意
  为之；B7 (tool result policy) 统一加 byte budget 时再考虑。
- **root 漂移**：`os.ReadFile` 只读 `cfg.RootDir/AGENTS.md`，root
  由 `--root` / `SGADK_ROOT` / cwd 决定。用户用错 root → 行为是
  "没注入"，不是 panic（跟 `loadSoulPrompt` 同语义）。

**回滚**：删 `{agents_md}` 占位符 + `loadAgentsMDPrompt` helper +
模板那一行——3 处。无 yaml flag。

---

## 配置变更 & yaml/CHANGELOG.md

**本步不动 yaml shape**——4 个子项全是代码内行为调整，无新字段、无重
命名、无默认值语义变化。`yaml/CHANGELOG.md` 不需要登记。

---

## Commit 拆分建议

按 AGENTS.md "Commit 粒度" 原则——每个 commit 一句话说清，行为变化与
重命名分开。

| # | 标题 | 范围 |
|---|---|---|
| 1 | `agent: toggle plan mode via PlanReminder middleware` | A1 全部（`PlanReminder` 新文件、`middleware_chain.go` 装配 + 删 `GetAgentMiddleWares` / `NewTodo()`、`MakeLeadAgent` 签名换 `getPlanMode func() bool`、`DeepAgentRuntime.planMode atomic.Bool` + `SetPlanMode`、Runtime 接口扩展、4 个 test stub 补 method、`/plan` 命令、phase3_test.go 断言迁移）。**单一行为变更**。 |
| 2 | `middlewares: emit TracePhaseTokens after model turns` | A4 在 trace.go / token_usage.go / middleware_chain.go 的改动。只动 agent 层，TUI 还没消费。**测试在这一步加：phase emit + TraceEvent shape**。 |
| 3 | `tui: footer shows running token total` | A4 的 TUI 端（model 加字段、handleTraceEvent 分支、renderFooter 加段、`formatTokenCount` helper）。**消费方独立成 commit**，配合 #2 一起 review。 |
| 4 | `tui: shimmer the streaming verb` | A5 全部（verbs.go 加 `renderShimmer`、styles.go 加 style、model.go 加 offset、update.go tick 推进、view.go 接入）。 |
| 5 | `agent: load AGENTS.md into system prompt` | A6 全部（`loadAgentsMDPrompt` helper + `GetSystemPrompt` 占位符 + 模板 1 行）。 |

> 如果 review 反馈说 #2 和 #3 合并更顺，可以折成一个 commit；保持独立
> 的好处是 #2 在 agent 层 self-contained，不依赖 TUI。

---

## 测试计划

按已有 test 文件就近补，避免新建文件。

| 子项 | 文件 | 新增 case |
|---|---|---|
| A1 | `backend/agent/middlewares/plan_reminder_test.go`（新建） | `TestAppendPlanInstruction`：getOn=true + msgs[0]=System("base") → msgs[0].Content == "base" + TodoInstruction；二次调用幂等（含 `<plan_mode>` 不再追加）；msgs[0] 非 system → 原样返回；getOn=false → 原样返回 |
| A1 | `backend/runtime/eino/runtime_test.go`（如无则新建 `plan_mode_test.go`） | `SetPlanMode(on=true)` 后 `r.planMode.Load() == true`；`r.runner` / `r.trace` 指针**未变**（确认无 rebuild）；history 长度不变 |
| A1 | `backend/cli/tui/update_test.go` 或新建 `plan_test.go` | `/plan on` 流转：input → handleBuiltin → planSetMsg → footerHint=`plan = on`；流式中 `/plan` 拒绝（system message: "finish or cancel..."）|
| A1 | `backend/agent/middleware_chain_phase3_test.go` | **删除**原 `len(GetAgentMiddleWares(true)) == 1` / `len(GetAgentMiddleWares(false)) == 0` 两块断言（函数已删）；新加一条：`getPlanMode := func() bool { return false }; chain := GetChatModelMiddlewares(...)`，断言 chain 中存在 `*middlewares.PlanReminder` 且其 index < `*middlewares.TodoReminder` 的 index |
| A4 | `backend/agent/middlewares/trace_test.go` | `TestTrace_EmitsTokensPhase`：mock consumer + `Trace.TokenSnapshot = func() TokenUsageStats { return TokenUsageStats{TotalTokens:1234} }`，After hook 触发，断言 consumer 收到 `Phase=TracePhaseTokens, Tokens.TotalTokens=1234`；`TokenSnapshot=nil` 时不发 |
| A4 | `backend/cli/tui/thinking_indicator_test.go` 或新建 `footer_test.go` | `m.tokenTotal=3450` 时 `renderFooter()` 包含 `"3.4k tokens"`；`m.tokenTotal=0` 时不含 `"tokens"` |
| A5 | `backend/cli/tui/verbs_test.go` | `TestRenderShimmer_WrapsAroundCycle`：固定 verb=`"Moonwalking…"`，offset 跨完整 cycle 一圈，断言至少一次 bright 段非空 + offset=0 时 bright 段为空（窗口在左侧外）|
| A5 | `backend/cli/tui/thinking_indicator_test.go` | 已有 case 验证 verb 出现；加一条 `m.streaming=true, shimmerOffset=5` 不 panic，输出仍含 `"Moonwalking"` 子串（shimmer 只切样式不丢字符）|
| A6 | `backend/agent/prompt_test.go` | `TestGetSystemPrompt_AgentsMDInjected`：tmp root 写 `AGENTS.md` 内容 `"hello rules"` → `GetSystemPrompt(...)` 输出含 `<workspace_conventions>\nhello rules\n</workspace_conventions>` 段；缺文件 / 文件为空 → 输出**不**含 `<workspace_conventions>` 子串（占位符替换为空，模板不留多余空行——跟 `{soul}` 同行为） |
| A6 | 同上 | `TestLoadAgentsMDPrompt_NilCfg`：直接调 `loadAgentsMDPrompt(nil)` 返回 `""`（防御性零值，跟 `loadSoulPrompt(nil)` 同语义） |

测试预算：6 个 file（多数是补 case，2 个新建），约 +260 行测试代码。

---

## 验收

PR 合并后，用户视角的变化（一句话归纳）：

- `/plan on` 打开后，模型回复里开始出现 `write_todos` 调用。
- footer 右下从 `kimi · /help · ctrl-c to quit` 变成
  `kimi · 2.3k tokens · /help · ctrl-c to quit`。
- 流式中 `Moonwalking…` 字样有一道高亮光带左右滚动。
- system prompt 里多了 `<workspace_conventions>` 段：仓库根 `AGENTS.md`
  全文（项目工程约定），永久驻留。
