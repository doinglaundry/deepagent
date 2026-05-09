# 实现方案：TUI 内嵌的模型 I/O 调试视图

**特性名**：`20260509-debug-tui-modelio` | **日期**：2026-05-09
**Owner**：agent / cli-tui
**状态**：Proposed（待评审）

## 概述

新增一个调试模式，启用后把每个 LLM turn 的真实输入（即将送入模型的完整消息切片）和输出（模型刚返回的 assistant message，含 tool calls）**就地塞回现有 TUI 的滚动历史里**——同一个 `viewport`、同一种滚动方式、同一套 markdown 渲染管线，只是多两类视觉上区别明显的消息类型。

不写 JSON 文件、不开新面板、不引入新二进制。debug 事件复用 TUI 流式 chunk 现有的 per-call channel 机制，沿同一条路径流回前端。

整个特性由一条运行时 `/debug` slash 命令开关。关闭状态下，整条路径只有一次 `ctx.Value` 查找——实际上是零开销。

## 目标

- **G1** 对每一次模型调用，原样展示送进 LLM 的最终消息切片（在 memory 注入、clarification、所有其他中间件改写之后）。
- **G2** 展示模型刚返回的新 assistant message（content + tool_calls），不重复回显已经显示过的历史。
- **G3** 通过 `/debug` slash 命令在会话中途随时开关；默认关闭，普通用户感知不到额外噪音。
- **G4** 关闭时零开销（每个 turn 至多一次 `context.Value` 查找）。
- **G5** 渲染在现有 bubbletea viewport 内，使用 dim 暗化样式让 debug 事件视觉上从属于真实对话。

## 非目标

- **NG1** 不做持久化日志文件。用户已明确否决"落 JSON 到磁盘"，要求只在 TUI 显示。（如果以后真的需要持久化，加一个 `Tee` consumer 即可，本设计无需改动。）
- **NG2** TUI 内部不做结构化查询/过滤 UI（jq 风格的检视），就是普通文本渲染。
- **NG3** 不新增 model 接口或 runtime 入口，复用 `eino.Runtime.ExecuteStream`。
- **NG4** 不做 turn 间深度 diff（比如"只显示自上一轮以来的增量"）。v1 每次 `before` 事件直接 dump 完整切片；v2 再考虑加 diff。
- **NG5** 不做远程/多 host 扇出，仅进程内单 channel。

## 设计

### 数据流

```
┌────────── agent goroutine（adk.Runner）──────────┐    ┌─ tea Update goroutine ─┐
│ ChatModelAgent + middleware chain                 │    │                        │
│  └─ Trace MW（Before/After 钩子）                 │    │                        │
│       consumer := getDebugConsumerFromContext(ctx)   │    │                        │
│       consumer.Send(DebugEvent{...})              │    │                        │
│         └─ teaProgramConsumer{prog}.Send          │    │                        │
│              prog.Send(DebugEvent{...}) ──────────┼────► Update 收到 middlewares.DebugEvent
│                                                   │    │   ↓                    │
└───────────────────────────────────────────────────┘    │ pushMessage(           │
        ↑                                                │   "debug-input" /      │
   ctx 携带的 consumer = teaProgramConsumer{prog}        │   "debug-output", ...) │
   （生命周期 = 一次 ExecuteStream 调用）                └────────────────────────┘
```

跨 goroutine 投递不自建 channel，**直接复用 bubbletea 内置的线程安全事件队列**——`*tea.Program.Send(msg)` 会把 msg 排进该队列，主循环像处理普通 `tea.Msg` 一样 dispatch 到 `Update`。这套设计天然消除了 `debugCh` / `waitForDebug` / 自我续接 / `defer close` 这一整套手搓 pump 的样板代码。

### 分层职责

| 层 | 新增/改动 | 职责 |
|---|---|---|
| **中间件**（`agent/middlewares`）| 新增 `debug.go` | `Trace` 中间件；`DebugEvent` / `DebugConsumer` 类型；`WithDebugConsumer` helper。 |
| **链装配**（`agent/middleware_chain.go`）| 改动 | 在 ChatModel 槽位倒数第二个位置注册 `NewTrace(rt.AgentName)`。**签名不变**——需要拿到 lead Trace 引用的下游通过 `middlewares.FindTrace` 自取。 |
| **Lead agent / subagents**（`agent/lead_agent.go`、`agent/subagents.go`）| 改动 | `MakeLeadAgent` 把链里造好的 `*Trace` 透传回 `NewDeepAgentRuntime`；subagent 递归路径丢弃这个返回值（每个 subagent 自己也有一个独立 `Trace`，`/clear` 不需要管它们——会随 ExecuteStream 一起结束）。 |
| **运行时**（`runtime/eino`）| 改动 | `DeepAgentRuntime` 多一个 `*Trace` 字段，`ClearHistory` 调 `Trace.ResetTurn()`，让 `/clear` 之后 turn 编号从 1 重新开始。 |
| **TUI 入口**（`cli/tui/tui.go`）| +1 行 | `tea.NewProgram(...)` 之后把 `prog` 回灌到 `Model`，让 consumer 能调 `prog.Send`。 |
| **TUI 管道**（`cli/tui/stream.go`）| 扩展 | `startStream` 接受可选 `DebugConsumer`；新增 `teaProgramConsumer` 把 `Send` 适配成 `prog.Send`。 |
| **TUI 状态**（`cli/tui/model.go`）| 扩展 | `Model.debug bool` + `Model.prog *tea.Program`；两个新的 `chatMessage.Role` 取值；格式化 helper。 |
| **TUI 主循环**（`cli/tui/update.go`）| 扩展 | `Update` 里多一条 `case middlewares.DebugEvent`；`handleBuiltin` 里加 `/debug` slash。 |
| **测试**（`agent/middlewares/debug_test.go`）| 新增 | 用 fake consumer 单测 `Trace`，覆盖开/关两种行为。 |

## 各组件详细规格

### 1. `backend/agent/middlewares/debug.go`

自包含：类型 + ctx helper + 中间件本体。仅依赖 eino + 标准库。

```go
package middlewares

import (
    "context"
    "sync/atomic"

    "github.com/cloudwego/eino/adk"
    "github.com/cloudwego/eino/schema"
)

const (
    DebugBefore = iota + 1
    DebugAfter
)

// DebugEvent is one half-turn snapshot.
//
//   Phase=DebugBefore: Messages = the entire slice the model is
//     about to consume (after every preceding middleware mutated it).
//   Phase=DebugAfter:  Messages = a 1-element slice holding the new
//     assistant message (content + tool_calls).
//
// Turn is monotonic per Trace instance, 1-indexed; Before increments,
// After reuses the same value so consumers can pair them up.
//
// AgentName tags the originating agent (lead or subagent), so when
// subagents recurse they don't render as a confusing series of
// duplicated "turn 1" / "turn 2" lines from the lead's point of view.
type DebugEvent struct {
    AgentName string
    Phase     int
    Turn      int
    Messages  []*schema.Message
}

type DebugConsumer interface {
    Send(DebugEvent)
}

type debugConsumerKey struct{}

func WithDebugConsumer(ctx context.Context, consumer DebugConsumer) context.Context {
    if consumer == nil {
        return ctx
    }
    return context.WithValue(ctx, debugConsumerKey{}, consumer)
}

func getDebugConsumerFromContext(ctx context.Context) DebugConsumer {
    s, _ := ctx.Value(debugConsumerKey{}).(DebugConsumer)
    return s
}

// Trace is a no-cost middleware unless a DebugConsumer is attached to
// the per-call ctx. When attached, it sends a DebugBefore event at the
// start of each model turn (with the full message slice, post-mutation
// by every preceding middleware) and a DebugAfter event at the end
// (with just the new assistant message). Each emitted DebugEvent is
// tagged with the owning agent's name so subagent runs interleaved on
// the same consumer remain visually distinguishable.
//
// MUST be registered immediately BEFORE Clarification (i.e. as the
// last read-only middleware). Both Before and After hooks dispatch in
// registration order; Clarification's After hook rewrites the assistant
// message in-place (clears ToolCalls, replaces Content with the
// question), so Trace has to run first to capture the model's raw
// response.
//
// The lead agent's Trace is also exposed by MakeLeadAgent so the
// runtime can call ResetTurn() from /clear; subagent Traces are
// short-lived (one per ExecuteStream) and don't need explicit reset.
type Trace struct {
    *adk.BaseChatModelAgentMiddleware
    agentName string
    turn      atomic.Int64
}

func NewTrace(agentName string) *Trace {
    return &Trace{
        BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
        agentName:                    agentName,
    }
}

// ResetTurn rewinds the per-Trace turn counter to 0 so the next
// BeforeModelRewriteState observes Turn=1. Safe to call from any
// goroutine. Wired to Runtime.ClearHistory so the user-facing
// /clear command produces a fresh "turn 1 input" labeling instead
// of continuing the pre-clear sequence (which would be visually
// confusing now that the chat history above it is gone).
func (t *Trace) ResetTurn() { t.turn.Store(0) }

func (t *Trace) BeforeModelRewriteState(
    ctx context.Context,
    state *adk.ChatModelAgentState,
    _ *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
    consumer := getDebugConsumerFromContext(ctx)
    if consumer == nil || state == nil {
        return ctx, state, nil
    }
    consumer.Send(DebugEvent{
        AgentName: t.agentName,
        Phase:     DebugBefore,
        Turn:      int(t.turn.Add(1)),
        // Copy the slice header — subsequent middlewares may still
        // mutate state.Messages even after the model returns. We
        // don't deep-copy each message; *schema.Message is treated
        // as immutable post-emit.
        Messages: append([]*schema.Message(nil), state.Messages...),
    })
    return ctx, state, nil
}

func (t *Trace) AfterModelRewriteState(
    ctx context.Context,
    state *adk.ChatModelAgentState,
    _ *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
    consumer := getDebugConsumerFromContext(ctx)
    if consumer == nil || state == nil || len(state.Messages) == 0 {
        return ctx, state, nil
    }
    consumer.Send(DebugEvent{
        AgentName: t.agentName,
        Phase:     DebugAfter,
        Turn:      int(t.turn.Load()),
        Messages:  []*schema.Message{state.Messages[len(state.Messages)-1]},
    })
    return ctx, state, nil
}

// FindTrace returns the *Trace embedded in a middleware list, or nil
// if absent. Used by MakeLeadAgent to pull the lead's Trace out of the
// chain *after* GetChatModelMiddlewares built it — keeps the chain
// builder strictly responsible for "build the chain" and nothing else.
func FindTrace(list []adk.ChatModelAgentMiddleware) *Trace {
    for _, mw := range list {
        if t, ok := mw.(*Trace); ok {
            return t
        }
    }
    return nil
}
```

### 2. `backend/agent/middleware_chain.go`

在装配函数末尾插入一行，**位置必须在 `Clarification` 之前**。**函数签名保持不变**——`GetChatModelMiddlewares` 的职责就是"按规则装配链"，不应该额外暴露内部某个 middleware 实例（破 SRP）。需要拿到 lead Trace 引用的下游通过 §1 末尾提供的 `middlewares.FindTrace` helper 自取，详见下一节。

```go
func GetChatModelMiddlewares(
    ctx context.Context, cfg config.Config, mem *MemoryAccessor, rt RuntimeContext,
) (middlewareList []adk.ChatModelAgentMiddleware) {
    // ... 既有的 memory / hitl / summarizer 中间件追加不变 ...

    middlewareList = append(middlewareList, middlewares.NewTrace(rt.AgentName))   // ← 新增,以 agent 名字命名
    middlewareList = append(middlewareList, middlewares.NewClarification())       // ← 必须最后,Trace 在它之前
    return
}
```

为什么不在这里直接把 `*Trace` 抛出去：装配函数的输入是 `(ctx, cfg, mem, rt)`，输出应该是"一条满足合同的 middleware 链"——这是个职责干净的纯函数。如果再多一个 `*Trace` 返回值，函数就同时承担了"装配链"和"暴露内部成员"两件事，每加一个想被外部访问的 middleware 都得再往返回值上挂一个指针，签名只会越长越脏。helper 路线把"按类型从链里捞出来"这件事从装配函数里剥离出去，代价只是 `MakeLeadAgent` 多调一行 `FindTrace`，签名层面零污染。

为什么不能放在最后：eino 框架里 `BeforeModelRewriteState` 和 `AfterModelRewriteState` **都按注册顺序**遍历调用（见 `cloudwego/eino/adk/wrappers.go` 的 `for _, handler := range w.handlers`）——不存在"After 反向触发"那种行为。

| 钩子 | Trace 在 Clarification **之前** | Trace 在 Clarification **之后** |
|---|---|---|
| `BeforeModelRewriteState` | 看到完整 state（Clarification 的 Before 是 no-op，位置不影响） | 同左，无差别 |
| `AfterModelRewriteState` | ✅ 抓到模型**原始**返回（含 ToolCalls） | ❌ Clarification 已经改写过 assistant 消息（清空 ToolCalls、把 Content 替成 question），Trace 拿到的是改写后的"伪输出" |

`Clarification` 自己的 doc 也写明了 "must always be the last"——它依赖自己是最后一个动 state 的中间件。`Trace` 只读不改，放在它之前不破坏这条不变量。

### 3. `backend/agent/lead_agent.go` + `backend/agent/subagents.go` + `backend/runtime/eino/deep_runtime.go`

唯一的目的：把链里那个 `*Trace` 一路透传到 `DeepAgentRuntime`，让 `/clear` 能调到 `ResetTurn()`。具体做法：`MakeLeadAgent` 用 `middlewares.FindTrace` 从 `GetChatModelMiddlewares` 返回的链里把 lead Trace 捞出来，再回传给上游。这样 `GetChatModelMiddlewares` 的签名一行不动，"装配"和"暴露成员"两件事彻底解耦。

**`lead_agent.go`** —— `MakeLeadAgent` 多返回一个 `*middlewares.Trace`（lead 链里那一个）：

```go
// 旧签名： func MakeLeadAgent(ctx, rt, cfg) (adk.Agent, error)
// 新签名：
func MakeLeadAgent(
    ctx context.Context, rt RuntimeContext, cfg config.Config,
) (adk.Agent, *middlewares.Trace, error) {
    // ... mem 构造不变 ...

    handlers := GetChatModelMiddlewares(ctx, cfg, mem, rt)
    trace := middlewares.FindTrace(handlers)            // ← 用 helper 从链里捞出来,签名零污染

    leadAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
        // ... 既有字段 ...
        Handlers: handlers,
    })
    if err != nil {
        return nil, nil, err
    }
    return leadAgent, trace, nil
}
```

**`subagents.go`** —— `buildNamedSubagents` 递归调 `MakeLeadAgent`，对返回的 `*Trace` 直接丢弃（subagent 自己那条 Trace 跟着 ExecuteStream 一起短命，不需要被 runtime 持有）：

```go
sub, _, err := MakeLeadAgent(ctx, subRT, cfg)  // ← _ 接住 trace 丢弃
```

**`deep_runtime.go`** —— `DeepAgentRuntime` 多一个字段 + `ClearHistory` 多一行：

```go
type DeepAgentRuntime struct {
    // ... 既有字段 ...
    trace *middlewares.Trace // lead agent's Trace; nil-safe
}

func NewDeepAgentRuntime(...) (Runtime, error) {
    // ... rt / mem / cfg 准备不变 ...
    leadAgent, trace, err := agent.MakeLeadAgent(ctx, rt, cfg)
    if err != nil {
        return nil, err
    }
    return &DeepAgentRuntime{
        // ... 既有字段 ...
        trace: trace,
    }, nil
}

func (r *DeepAgentRuntime) ClearHistory() {
    r.history = nil
    if r.trace != nil {
        r.trace.ResetTurn() // ← 新增
    }
}
```

这条 "FindTrace + MakeLeadAgent 透传" 路线的几个关键性质：

- **职责切割**：装配链（`GetChatModelMiddlewares`）和"按类型从链里捞出某个 middleware"（`FindTrace`）是两件事，分别住在两个函数里——前者签名永远稳定；新加任何想被外部访问的 middleware 也只需要再加一个 `FindXxx` helper，不用动 `GetChatModelMiddlewares`。
- **类型断言只发生一次**：`FindTrace` 内部做一次 `mw.(*Trace)` 类型断言（O(链长)），把结果以稳定的 `*Trace` 形式交出去；下游（runtime / subagent build）拿到后不再做任何断言或反射。
- **subagent Trace 不会被误触**：subagent 路径上 `buildNamedSubagents → MakeLeadAgent` 同样会构造一个 `*Trace`，但 `_` 接住后立刻丢弃；只有 lead Trace 被存进 runtime，所以 `/clear` 只重置 lead 的计数器。
- **改动表面积**：1 个新 helper（`FindTrace`，~7 行）+ 1 个调用方（`MakeLeadAgent` 多调一行）+ 2 个上游签名变化（`MakeLeadAgent` 返回值多 `*Trace`、`NewDeepAgentRuntime` 接 3 元组）。`GetChatModelMiddlewares` 一行不动。

### 4. `backend/cli/tui/tui.go`

唯一的改动是把 `*tea.Program` 回灌给 `Model`——这一步发生在 `tea.NewProgram(...)` 之后、`prog.Run()` 之前。bubbletea 官方鼓励这种循环引用模式（专门为"从外部 goroutine 发自定义 msg"设计），生命周期天然绑死，无 GC 风险。

```go
func Run(rt eino.Runtime) error {
    // ...（既有的 TTY 检查不动）...
    m, err := New(rt)
    if err != nil {
        return err
    }
    prog := tea.NewProgram(m, /* 既有 opts 不动 */)
    m.prog = prog // ← 新增：让 cross-goroutine consumer 能调 prog.Send
    _, err = prog.Run()
    return err
}
```

### 5. `backend/cli/tui/stream.go`

只两处增量，**chunk 流的既有 channel 管道完全不动**。`DebugEvent` 直接复用作 `tea.Msg`（bubbletea 的 `tea.Msg` 就是 `interface{}`，任意类型都能塞）。

**(a)** 新增一个 4 行的适配器，把 `DebugConsumer.Send` 桥接到 `prog.Send`：

```go
type teaProgramConsumer struct{ p *tea.Program }

func (c teaProgramConsumer) Send(ev middlewares.DebugEvent) {
    c.p.Send(ev)
}
```

**(b)** `startStream` 多收一个 `DebugConsumer` 参数；非 nil 时挂到 ctx 上。函数体其他部分（chunkCh / doneCh / goroutine 启动 / return）一行不改：

```go
func startStream(rt eino.Runtime, prompt string, consumer middlewares.DebugConsumer) (
    <-chan string, context.CancelFunc, tea.Cmd,
) {
    // ... 既有的 chunkCh、doneCh、ctx/cancel 创建不变 ...

    if consumer != nil {                                  // ← 新增 3 行
        ctx = middlewares.WithDebugConsumer(ctx, consumer)
    }

    // ... 既有的 goroutine 启动 + awaitDone + return 不变 ...
}
```

为什么这点改动就够了：

- consumer 为 nil 时，`Trace` 中间件一次 `ctx.Value` 查到 nil 立刻短路 → 关闭态零开销。
- consumer 非 nil 时，整条路径是 `Send → prog.Send → bubbletea 内部队列 → Update` 主循环——全程线程安全，不需要自建 channel、`waitForDebug`、`defer close` 这套样板。
- program 已退出时 `prog.Send` 静默丢弃，不 panic / 不阻塞，cancel 路径自然干净。

### 6. `backend/cli/tui/model.go`

新增两个字段、两条 `chatMessage.Role` case，加几个格式化 helper。

```go
// Model …
type Model struct {
    // … existing …
    debug bool
    prog  *tea.Program // injected by Run() in tui.go after tea.NewProgram
}

// chatMessage.Role values: existing "user" | "assistant" | "system",
// plus new "debug-input" | "debug-output".
```

`renderMessage` 多两条 case，把 `msg.Content` 用 dim 样式包起来，前面加内联标记。debug 内容**不**走 markdown 渲染（它已经是结构化的纯文本了）。

```go
case "debug-input":
    return debugInputMarkerStyle.Render("▶ ") +
        debugBodyStyle.Render(msg.Content)
case "debug-output":
    return debugOutputMarkerStyle.Render("◀ ") +
        debugBodyStyle.Render(msg.Content)
```

格式化 helper（同 package）：

```go
func formatDebugInput(ev middlewares.DebugEvent) string {
    var sb strings.Builder
    var totalBytes int
    for _, m := range ev.Messages {
        totalBytes += len(m.Content)
    }
    // [agentname] 前缀让父 + 子 agent 的事件交织时一眼可分辨
    // （subagent 各自有独立 Trace，turn 编号互不相干，不带 agent 标识
    // 时就会出现"为什么有两条 turn 1 input"的视觉错觉）。
    fmt.Fprintf(&sb, "[%s] turn %d input · %d messages · %s\n",
        ev.AgentName, ev.Turn, len(ev.Messages), humanBytes(totalBytes))
    for _, m := range ev.Messages {
        fmt.Fprintf(&sb, "[%s] %s\n", m.Role, truncate(m.Content, debugBodyMaxBytes))
        for _, tc := range m.ToolCalls {
            fmt.Fprintf(&sb, "  ↳ tool_call %s(%s)\n",
                tc.Function.Name,
                truncate(tc.Function.Arguments, debugToolArgMaxBytes))
        }
    }
    return strings.TrimRight(sb.String(), "\n")
}

func formatDebugOutput(ev middlewares.DebugEvent) string {
    if len(ev.Messages) == 0 {
        return ""
    }
    last := ev.Messages[0]
    var sb strings.Builder
    fmt.Fprintf(&sb, "[%s] turn %d output\n", ev.AgentName, ev.Turn)
    if c := strings.TrimSpace(last.Content); c != "" {
        fmt.Fprintf(&sb, "[%s] %s\n", last.Role, c)
    }
    for _, tc := range last.ToolCalls {
        fmt.Fprintf(&sb, "  ↳ tool_call %s(%s)\n",
            tc.Function.Name,
            truncate(tc.Function.Arguments, debugToolArgMaxBytes))
    }
    return strings.TrimRight(sb.String(), "\n")
}
```

常量：

```go
const (
    debugBodyMaxBytes    = 4 << 10 // 4 KB per-message-content cap
    debugToolArgMaxBytes = 1 << 10 // 1 KB per tool-call arguments cap
)
```

`truncate(s, n)`：`len(s) <= n` 直接返回 `s`，否则返回 `s[:n] + "…(N more bytes)"`。`humanBytes(n)`：把 `1234` 格式化成 `1.2 KB`。两个函数各 ≤ 10 行。

### 7. `backend/cli/tui/update.go`

三处增量：

**(a)** `submit` 在开启 debug 时构造一个 `teaProgramConsumer` 透传给 `startStream`，**不再 batch 任何 `waitForDebug`**——debug 事件由 bubbletea 内部队列直接喂回 `Update`。

```go
func (m *Model) submit(text string) (tea.Model, tea.Cmd) {
    if cmd, handled := m.handleBuiltin(text); handled {
        return m, cmd
    }
    m.pushMessage("user", text)
    m.streaming = true
    m.streamBuf.Reset()
    m.lastErr = nil

    var consumer middlewares.DebugConsumer
    if m.debug {
        consumer = teaProgramConsumer{p: m.prog}
    }

    ch, cancel, awaitDone := startStream(m.rt, text, consumer)
    m.chunkCh = ch
    m.cancel = cancel
    return m, tea.Batch(waitForChunk(ch), awaitDone, m.spin.Tick)
}
```

**(b)** `Update` switch 多一条分支。`handleDebug` 末尾返回 `nil` 即可——下一个 debug 事件靠 bubbletea 内部队列继续投递，不需要任何"自我续接"。

```go
case middlewares.DebugEvent:
    return m.handleDebug(msg)
```

```go
func (m *Model) handleDebug(ev middlewares.DebugEvent) (tea.Model, tea.Cmd) {
    switch ev.Phase {
    case middlewares.DebugBefore:
        m.pushMessage("debug-input", formatDebugInput(ev))
    case middlewares.DebugAfter:
        m.pushMessage("debug-output", formatDebugOutput(ev))
    }
    return m, nil
}
```

**(c)** `handleBuiltin` 新增 `/debug` 分支：

```go
case "debug":
    arg := strings.TrimSpace(strings.TrimPrefix(text, "/debug"))
    switch strings.ToLower(arg) {
    case "", "toggle":
        m.debug = !m.debug
    case "on":
        m.debug = true
    case "off":
        m.debug = false
    default:
        m.pushMessage("system", "usage: /debug [on|off|toggle]")
        return nil, true
    }
    state := "off"
    if m.debug {
        state = "on"
    }
    m.pushMessage("system", fmt.Sprintf("debug = %s", state))
    return nil, true
```

`builtinHelp()` 文案多一条：
`/debug [on|off|toggle] — show/hide model input & output per turn`。

### 8. `backend/agent/middlewares/debug_test.go`

四个 case，约 70 行。所有 case 都用 `NewTrace("test-agent")` 构造 trace：

| 测试 | 准备 | 断言 |
|---|---|---|
| `TestTrace_NoConsumerIsNoop` | 构造 `Trace`，用裸 ctx（无 consumer）调用钩子。 | 不 panic；两个钩子都原样返回 state。 |
| `TestTrace_SendsBeforeAndAfter` | 通过 `WithDebugConsumer` 挂上 `recordingConsumer`（一个标准库 slice）；构造 `Trace`；以一个含 3 条消息的 state 先调 Before 再调 After。 | consumer 收到 2 个事件：Before 时 `len(Messages)==3`，After 时 `len(Messages)==1`，两者 `Turn==1`，两者 `AgentName=="test-agent"`。 |
| `TestTrace_TurnMonotonic` | 挂 consumer；依次调 Before / After / Before / After。 | Turn 值依次是 1,1,2,2。 |
| `TestTrace_ResetTurn` | 挂 consumer；调 Before / After 各一次（Turn=1）→ 调 `trace.ResetTurn()` → 再调 Before / After。 | 第二轮 Before 的 Turn 重新从 1 开始（不是 2）。 |

`recordingConsumer` 就是 `type recordingConsumer struct{ ev []DebugEvent }` 加一个 `(s *recordingConsumer) Send(...)` 方法——单 goroutine 测试，不需要并发原语。

## UX 细节（实例演练）

用户操作流程：

```
❯ /debug on
• debug = on

❯ tell me a joke
▶ [DeerFlow] turn 1 input · 4 messages · 4.1 KB
[system] You are a helpful AI assistant. You have access to ... (truncated, 3.8 KB more)
[user] tell me a joke
…（assistant 流式返回中）
◀ [DeerFlow] turn 1 output
[assistant] Why don't scientists trust atoms? Because they make up everything!
⏺ Why don't scientists trust atoms? Because they make up everything!

❯ another one with a tool
▶ [DeerFlow] turn 2 input · 6 messages · 4.3 KB
[system] You are a helpful AI assistant. You have access to ... (truncated, 3.8 KB more)
[user] tell me a joke
[assistant] Why don't scientists trust atoms? …
[user] another one with a tool
◀ [DeerFlow] turn 2 output
[assistant] Let me look that up for you.
  ↳ tool_call shell({"command":"fortune"})
…
```

如果 lead agent 触发了 subagent（例如调用 `transfer_to_agent` 进入 researcher），subagent 自己也有一条 Trace，事件会跟父事件交织进同一份 consumer：

```
▶ [DeerFlow] turn 3 input · 8 messages · 5.2 KB
…
◀ [DeerFlow] turn 3 output
[assistant]
  ↳ tool_call transfer_to_agent({"agent_name":"researcher", ...})
▶ [researcher] turn 1 input · 5 messages · 3.1 KB     ← 子 agent 自己的 Trace，Turn 从 1 起
…
◀ [researcher] turn 1 output
…
```

`[agentname]` 前缀就是为了这种交织场景：没有它的话用户会看到一系列让人困惑的"为什么有两条 turn 1"。

色彩/样式预算（lipgloss）：

- `debugInputMarkerStyle` —— 加粗，淡蓝
- `debugOutputMarkerStyle` —— 加粗，淡品红
- `debugBodyStyle` —— `Faint(true)`，不走 markdown
- 既有 `userPrefixStyle` / `assistantPrefixStyle` —— 不动

dim 暗化样式是给用户的视觉信号：「这是元信息，不感兴趣可以滑过去」。真正的对话内容保持全亮度。

## 决策与权衡

| 决策 | 选择 | 理由 |
|---|---|---|
| Trace 在链中的位置 | Clarification **之前**（链尾倒数第二个） | After 钩子按注册顺序触发；Clarification 的 After 会改写 assistant 消息（清 ToolCalls、Content 替成 question），Trace 必须先于它跑才能抓到模型的原始输出。Before 侧两个位置等价。 |
| Before 时切片是否拷贝 | 拷贝（`append([]*schema.Message(nil), …)`） | 后续中间件还可能改写切片，消费者需要稳定视图。 |
| 每条消息是否深拷贝？ | 不做 | `*schema.Message` 视为不可变；况且对系统 prompt 体量大的切片做深拷贝代价高。 |
| After payload | 只发最后一条消息 | 之前的消息已经在前一轮 Before 里发过了；每次 After 都发完整切片会造成滚动历史 2× 重复。 |
| 默认状态 | 关 | 大多数会话用不上；`/help` 里写明开法即可。 |
| 单条消息正文上限 | 4 KB | 当前系统 prompt 在 2-4 KB 量级；4 KB 能完整展示一次真实 prompt，又不会让后续 turn 撑爆一屏。 |
| tool_call 参数上限 | 1 KB | 普通工具调用绰绰有余；偶发 100 KB 参数（如 `write_file` 大 blob）会带尾部标记。 |
| 持久化 | 不做（事件仅经 `prog.Send`） | 用户明确要求："不要写在 JSON 文件里"。后续要加写文件的话，新增一个 `Tee` consumer 即可，不动本设计。 |
| 开关作用域 | TUI Model state | 进程内、瞬时、跨 `/clear` 仍存活、随二进制退出而消失。 |
| 跨 goroutine 投递 | `tea.Program.Send` | bubbletea 内置线程安全 FIFO 队列，专门面向"外部 goroutine 注入自定义 msg"的场景。比自建 channel + `waitForXxx` 自我续接路线少 ~37 行样板代码，并且 program 退出后 Send 静默丢弃，无 panic / 无泄漏。 |
| `/clear` 时是否重置 turn 计数器 | 重置（`Trace.ResetTurn()`） | 用户对 `/clear` 的心智是"清空一切重新开始"，turn 编号继续递增会反直觉——清屏后看到 `turn 7 input` 而上面没有 turn 1~6 的渲染历史，让人怀疑是否漏帧。代价：runtime 多持一个 `*Trace` 指针 + 4 行 plumbing。 |
| subagent 事件如何区分 | `DebugEvent.AgentName` + `[agentname]` 前缀渲染 | `buildNamedSubagents` 给每个 subagent 单独装一条中间件链，每个 subagent 各自有独立 `Trace` + 独立 turn 计数器；交织进同一份 consumer 时，没有 agent 标识就会出现"为什么有多条 turn 1"的视觉错觉。代价：`DebugEvent` 多 1 字段、`NewTrace` 多 1 参数、format helper 多 1 个格式 verb。 |

## 实施计划

| 步骤 | 触碰文件 | 估算 LoC | 验证方式 |
|---|---|---|---|
| 1. 落地 `Trace` 中间件 + `FindTrace` helper + 测试（含 `AgentName` / `ResetTurn` / 4 个 case） | `agent/middlewares/debug.go`、`agent/middlewares/debug_test.go` | +155 | `go test ./backend/agent/middlewares/...` |
| 2. 注册 `NewTrace(rt.AgentName)` 进链（**签名不变**） | `agent/middleware_chain.go` | +1 | `go build ./...` |
| 3. 串入 runtime：`MakeLeadAgent` 用 `FindTrace` 捞 trace 并多返回；`buildNamedSubagents` 丢弃返回；`DeepAgentRuntime` 多字段 + `ClearHistory.ResetTurn` | `agent/lead_agent.go`、`agent/subagents.go`、`runtime/eino/deep_runtime.go` | +25 | `go build ./...`；手测 `/clear` 后 turn 编号从 1 重新开始 |
| 4. `tui.go` 回灌 `m.prog` + `stream.go` 加 `teaProgramConsumer` 适配器、`startStream` 多接受一个可选 consumer | `cli/tui/tui.go`、`cli/tui/stream.go` | +20 | TUI 编译通过；既有流程仍正常 |
| 5. 加 `Model.debug` / `Model.prog` 字段 + 格式化 helper（含 `[agentname]` 前缀）+ 新 render case | `cli/tui/model.go` | +55 | TUI 编译通过 |
| 6. 加 `case middlewares.DebugEvent` + `/debug` slash + help 文案 | `cli/tui/update.go` | +25 | 手测：`eino-tui` → `/debug on` → 提问 → 看到两块新 block |
| 7. 样式精修（新 role 的 lipgloss 样式） | `cli/tui/styles.go` | +10 | 手测肉眼看 |

每一步都可以独立提交一次 commit；(1)(2)(3) 可以先合，作为一个 no-op 特性（debug 关时整条路径仍然只走一次 `ctx.Value` 短路），(4-7) 再点亮消费侧。

总改动量估计：**+291 LoC**（含两个新增能力：`/clear` 重置 turn + 子 agent 区分）。无任何上游 API 破坏；下游签名变化仅限 `MakeLeadAgent` 这一个内部函数（多返回 `*Trace`），`GetChatModelMiddlewares` 签名一行不动。

## 测试方案

### 自动化

- `agent/middlewares/debug_test.go` —— 按上面表格 4 个 case，外加 `TestFindTrace` 一组（命中 / 未命中 / 空 list 三个子 case）。
- `agent/middleware_chain_phase3_test.go` —— 在既有的"期望类型列表"上扩一条，断言 `*middlewares.Trace` 出现在 `chain.ChatModel` 的 `len-1` 索引处。

### 手测（TUI smoke）

1. `go run ./cmd/eino-tui`（或对应的入口名字）。
2. 验证默认状态：提问、收到正常回答、看不到 debug block。
3. `/debug on` —— 系统回复 `debug = on`。
4. 提一个问题，验证：
   - assistant 流式回复**之前**出现一条 `▶ turn 1 input · N messages · K KB` block。
   - assistant 完成**之后**出现一条 `◀ turn 1 output` block。
   - 两块视觉上比 user/assistant 的内容暗。
5. 追问一句。验证 Turn 计数到 2；Before 的消息切片增加约 2 条。
6. 提一个会触发 tool_call 的问题。验证 After block 含 `↳ tool_call <name>(<args>)` 行。
7. `/debug off` —— 确认下一轮没有 block。
8. `/debug toggle` 两次 —— 确认开/关切换正确。
9. debug 开启时流式响应中按 Ctrl-C：无 goroutine 泄漏（runtime goroutine 经 `ctx.Done` 退出；`teaProgramConsumer.Send` 即使在退出窗口内还想 Send，bubbletea 也会静默丢弃）。

### 边界情况

- **超长系统 prompt > 4 KB**：截断标记可见，不 panic。
- **空内容消息**（如 Content 为空的 tool result）：渲染成 `[tool] ` 加空 body，不崩。
- **Before 中途 cancel**：`teaProgramConsumer.Send` 是非阻塞的（`prog.Send` 内部入队即返），即便 program 已停止 bubbletea 也只是丢弃 msg，不泄漏、不 panic。
- **多次连续 Run**：`Trace.turn` 计数器是 per-middleware-instance；同一 runtime 生命周期内单调递增。`/clear` 时由 `Runtime.ClearHistory` 调 `Trace.ResetTurn()` 显式归零（详见 §3），下一轮 turn 从 1 重新开始。
- **Subagent 跨层调用**：subagent 各自有独立 `Trace` 和独立 turn 计数器；事件交织进同一份 consumer 时靠 `[agentname]` 前缀区分。subagent 的 Trace 不会被 `/clear` 触及（runtime 只持 lead Trace 引用），但因为 subagent 是 short-lived（一次 ExecuteStream 内构造、用完即弃），下一轮 ExecuteStream 会重新构造一个 Trace，turn 自然从 1 起算，不需要单独的 reset 通路。

## Open questions（待拍板）

1. **格式风格** —— 现在是结构化 bullet list 写法。要不要做成一行一 JSON（机器友好但可读性差）？设计里挑了 bullet 走人类可读路线。后面有 jq 风格过滤需求，要么拿持久化的滚动历史 grep，要么后续再加一个 `Tee` JSON consumer。
2. **`/debug full` vs 默认截断** —— 要不要再做一个二级 verbosity 关掉 4 KB 上限？v1 不做，等真有人提需求再说。

> 历史决议：
> - **`/clear` 时重置 turn 计数器** —— 已决定重置，详见决策表 + §3 runtime 串入。
> - **subagent debug 事件如何呈现** —— 已决定加 `DebugEvent.AgentName` + `[agentname]` 前缀，让父子 agent 在同一份 consumer 上交织时仍可区分。详见决策表 + §1 / §6。

## 后续工作（v1 范围之外）

- **Tee consumer** —— 第二个 `DebugConsumer`，并行把 JSONL 落盘。加法很轻：用 `multiConsumer{a, b DebugConsumer}` 套两个 consumer 即可。
- **Diff 模式** —— Before payload 只发自上次 Before 以来新增的消息。能极大砍掉长对话的滚动噪音。
- **Token 计数显示** —— 等 token-usage 中间件能跟踪 per-message token 后，把 token 数显示在字节数旁边。
- **Replay** —— debug 事件持久化成 session 文件，单独写一个 CLI 离线回放渲染。对长 agent run 的事后分析有用。
