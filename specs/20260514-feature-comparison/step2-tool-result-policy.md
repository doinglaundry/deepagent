# Step 2: 统一 Tool Result Policy

> Step 1 已落 plan-mode 等"已写完待开关"项目；本 spec 处理 design.md §2 B7。

## 1. 背景与现状

### 1.1 当前数据流

每个内置工具（`backend/agent/tools/*.go`）直接返回 `(string, error)`：

- `read_file` → cat -n 风格全文，没有 byte cap
- `glob` → 路径列表，没有 head/tail
- `grep` → 内置 `applyPagination`，但只在 head_limit 显式传入时生效
- `execute` → `stdout+stderr+exit_code`，无截断
- `shell` / `await_shell` → 已 hardcode `truncateToolOutput(..., 64*1024)`
- `read_lints` → 同上 64KB
- `write_file` / `edit_file` / `delete_file` / `apply_patch` → 单行 "Updated/Deleted/Applied" 字符串，已经天然摘要
- `semantic_search` → top-10 硬截
- `ask_clarification` → 由 middleware 拦截，本体不走

下游有两条独立链路读这个 `string`：

| 链路 | 现在怎么读 |
|------|-----------|
| Model（agent 下一轮 prompt） | `middlewares.ToolErrorHandling` 在 error 时把错误信息包成 `"Error executing tool %q: %s"`；成功直接透传 raw string |
| TUI 渲染 | `backend/cli/tui/tool_block.go` 的 `formatArgsLine` + `renderToolBlock` 拿到同一个 raw string，按工具名做 ad-hoc JSON 解析摘要给人看 |

**关键问题**：摘要只发生在 TUI 端，**model 看到的还是原始 raw 文本**。当
`execute` 返回 200 KB shell output、`read_file` 返回 8000 行文件、`grep` 命
中几千行时，token 直接灌进上下文。同时 `formatArgsLine` 用工具名 switch + 自
己 `json.Unmarshal` args 的临时方案，跟 tool 实现脱钩、容易漂。

### 1.2 helixent 对照

`src/agent/tool-result-policy.ts` + `tool-result-runtime.ts`：每个工具有
一个 `ToolResultPolicy { preferSummaryOnly, includeData, maxStringLength,
uiSummaryOnly }`，runtime 把原始返回 normalize 成
`StructuredToolResult { ok, summary, data?, error?, code? }`，**写回给 model
的就是 policy 加工后的 JSON 字符串**，UI 端通过 `summarizeToolResultText`
拿 `summary` 字段渲染。Model-facing payload 和 UI-facing summary **彻底
分离**：UI 拿短摘要，model 拿（结构化的）截断版数据，错误统一带 `code`。

收益：

- **省 token**：长输出按字段单独截断而非"末尾粘 [truncated]"
- **防 prompt 注入**：model 看到的是 JSON 字段（`summary` / `data`），自由
  文本被装进 `data` 不会被当作指令
- **解耦 UI**：TUI 不再需要 `formatArgsLine` 的工具名 switch

## 2. 目标架构

### 2.1 总体思路

- tool 函数本体**继续返回 `(string, error)`**，签名不动（保持 eino
  `utils.InferTool` 兼容）。
- 在 agent 层加一个 `ToolResultPolicy` 表 + 一个 middleware `ToolResultPolicyMW`：
  - 拦截 `InvokableToolCallEndpoint`
  - 拿到 raw `(string, error)`
  - 按 policy 产出 `StructuredResult { ModelPayload, UISummary }`
  - **向 agent 层返回 `ModelPayload`**（这是 model 真正看到的）
  - **通过 `ToolContext` 上挂的 channel 把 `UISummary` 旁路给 TUI**
- 替换现有 `ToolErrorHandling` 中间件 —— policy 已经覆盖 error → 结构化字符串
  的转换路径，没必要再压一层。

### 2.2 与现有 middleware 的边界

middleware_chain 顺序（外 → 内）调整：

```
NewToolCallObservability   # 仍然最内层,看 raw size
NewToolResultPolicyMW      # 新增,替代 NewToolErrorHandling
patchtoolcalls / ...       # 不变
```

`tool_observability` 不动 —— 它要观察的就是原始 size。`tool_error` 这个
旧 middleware **删掉**，错误路径并入 policy（error → `StructuredResult.ok=false`
→ JSON 序列化）。

### 2.3 TUI 端如何拿 UISummary

最直接：`ToolContext` / tool option 不暴露用户态 channel；改在
**Trace 事件流**里加一类 `tool_result` 事件，TUI 通过现有的
`runtime/eino/events.go` 订阅即可（已经在订阅 `tool_call` / `tool_result`，
只需扩字段）。`tool_block.go` 拿到事件里附带的 `UISummary`，**不再 unmarshal
raw string**。

## 3. 代码骨架

新建 `backend/agent/toolresult/`，单一 package：

### 3.1 policy.go —— 数据 + 主流程

```go
// Package toolresult applies per-tool size/shape policy to raw tool output
// so model-facing payload (token-bounded JSON) and UI-facing summary
// (short human line) come from one place instead of being re-derived on
// each side.
package toolresult

// Policy is pure data: how to shape one tool's result for the model and
// for the UI. Behaviour lives in apply.go.
type Policy struct {
	MaxBytes        int  // ModelPayload hard cap (post-JSON). 0 = no cap.
	PreferSummary   bool // Drop Data field; ship only Summary to the model.
	IncludeData     bool // Keep raw output as Data when within budget.
	UISummaryOnly   bool // TUI should NOT show the body even on expand.
	HeadLines       int  // For line-oriented tools, keep first N lines.
	SummaryBuilder  func(raw string) string // Optional override; nil → buildDefaultSummary.
}

// Result is what flows past the policy: ModelPayload is what gets shoved
// into the next assistant turn; UISummary is what the TUI renders. Err is
// kept non-nil ONLY when the failure is unrecoverable for the agent loop
// (tool-side error converts to ok=false JSON inside ModelPayload, NOT here).
type Result struct {
	ModelPayload string
	UISummary    string
	Err          error
}

// getPolicy returns the policy for tool name; falls back to defaultPolicy.
// Single source of truth for per-tool tuning — see §4 for the table.
func getPolicy(name string) Policy {
	if p, ok := policies[name]; ok {
		return p
	}
	return defaultPolicy
}
```

### 3.2 apply.go —— 行为

```go
package toolresult

import (
	"encoding/json"
	"fmt"
	"strings"
)

// applyPolicy is the single funnel: (raw, err) -> Result. Called by the
// middleware in the only place we wrap a tool endpoint. No method, no
// receiver — the function body reads top-to-bottom.
func applyPolicy(name, raw string, err error) Result {
	policy := getPolicy(name)
	if err != nil {
		return buildErrorResult(name, err, policy)
	}
	return buildSuccessResult(name, raw, policy)
}

// buildSuccessResult: head-trim → summary → JSON serialise → byte cap.
// All four steps happen inline; splitting them out would add a layer
// for no reader benefit.
func buildSuccessResult(name, raw string, policy Policy) Result {
	body := raw
	if policy.HeadLines > 0 {
		body = headLines(raw, policy.HeadLines)
	}
	summary := buildSummary(name, raw, policy)

	payload := map[string]any{"ok": true, "summary": summary}
	if policy.IncludeData && !policy.PreferSummary {
		payload["data"] = body
	}
	encoded := mustJSON(payload)
	if policy.MaxBytes > 0 && len(encoded) > policy.MaxBytes {
		encoded = mustJSON(map[string]any{"ok": true, "summary": summary})
	}
	return Result{ModelPayload: encoded, UISummary: summary}
}

func buildErrorResult(name string, err error, policy Policy) Result {
	summary := fmt.Sprintf("%s failed: %s", name, err.Error())
	payload := mustJSON(map[string]any{
		"ok":      false,
		"summary": summary,
		"error":   err.Error(),
	})
	if policy.MaxBytes > 0 && len(payload) > policy.MaxBytes {
		payload = payload[:policy.MaxBytes]
	}
	return Result{ModelPayload: payload, UISummary: summary}
}

// buildSummary delegates to policy override when set; otherwise picks the
// first non-empty line + a "(+N lines)" tail.
func buildSummary(name, raw string, policy Policy) string {
	if policy.SummaryBuilder != nil {
		return policy.SummaryBuilder(raw)
	}
	return buildDefaultSummary(raw)
}

func buildDefaultSummary(raw string) string {
	lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	if len(lines) == 0 {
		return ""
	}
	if len(lines) == 1 {
		return lines[0]
	}
	return fmt.Sprintf("%s (+%d lines)", lines[0], len(lines)-1)
}

func headLines(raw string, n int) string {
	if n <= 0 {
		return raw
	}
	lines := strings.SplitN(raw, "\n", n+1)
	if len(lines) <= n {
		return raw
	}
	return strings.Join(lines[:n], "\n") + fmt.Sprintf("\n[+%d more lines]", len(lines)-n)
}

func mustJSON(v any) string {
	out, _ := json.Marshal(v) // map[string]any with string values: cannot fail.
	return string(out)
}
```

### 3.3 table.go —— 每工具配置

参考 §4，按工具名 → Policy 编织成 `var policies = map[string]Policy{...}`，
和 `defaultPolicy` 一起放这个文件。表格驱动，无 if/else 分支。

### 3.4 middleware：`backend/agent/middlewares/tool_result_policy.go`

```go
package middlewares

import (
	"context"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"

	"eino-cli/backend/agent/toolresult"
	"eino-cli/backend/runtime/eino"
)

// ToolResultPolicy wraps every InvokableToolCallEndpoint so the model sees
// a structured, byte-bounded JSON payload while the UI gets a one-line
// summary via the Trace event stream. Replaces the old ToolErrorHandling
// middleware — error paths fold into the policy as ok=false JSON.
type ToolResultPolicy struct {
	*adk.BaseChatModelAgentMiddleware
	emitUISummary func(toolName, summary string) // see runtime/eino/events.go
}

func NewToolResultPolicy(emit func(string, string)) *ToolResultPolicy {
	return &ToolResultPolicy{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		emitUISummary:                emit,
	}
}

func (m *ToolResultPolicy) WrapInvokableToolCall(
	_ context.Context,
	endpoint adk.InvokableToolCallEndpoint,
	tCtx *adk.ToolContext,
) (adk.InvokableToolCallEndpoint, error) {
	name := ""
	if tCtx != nil {
		name = tCtx.Name
	}
	return func(ctx context.Context, args string, opts ...tool.Option) (string, error) {
		raw, err := endpoint(ctx, args, opts...)
		result := toolresult.ApplyPolicy(name, raw, err)
		if m.emitUISummary != nil {
			m.emitUISummary(name, result.UISummary)
		}
		return result.ModelPayload, result.Err
	}, nil
}
```

`emit` 由 `runtime/eino/deep_runtime.go` 注入 —— 把 `(name, summary)` 推到
现有的 Trace event 队列里，TUI 通过 `runtime/eino/events.go` 消费。

> `ApplyPolicy` 是 `applyPolicy` 的导出版（package 外部调用入口），逻辑同
> §3.2。如果只有 middleware 一个外部 caller，直接 export `ApplyPolicy`、删掉
> 内部小写版本即可，避免双套别名。

## 4. 每工具 Policy 推荐表

| 工具 | MaxBytes | PreferSummary | IncludeData | HeadLines | 说明 |
|------|---------:|--------------:|------------:|----------:|------|
| `read_file` | 64 KB | false | true | 0 | 大文件让 model 读完整内容；超 64 KB 才退化为 summary-only（model 应该用 offset/limit 分页） |
| `ls` | 4 KB | true | false | 0 | 文件列表对 model 几乎没价值，summary `"42 entries: a.go, b.go, ..."` 就够 |
| `glob` | 4 KB | true | false | 0 | 同上；如果路径都重要，model 自己再 read |
| `grep` | 8 KB | false | true | 50 | head 50 行 + summary 给 model；超长截断 |
| `rg` | 8 KB | false | true | 50 | 同 grep |
| `execute` | 16 KB | false | true | 200 | stdout+stderr 各裁；保留 exit code 在 summary |
| `shell` | 16 KB | false | true | 200 | 同上 |
| `await_shell` | 16 KB | false | true | 200 | 已有 64 KB hardcode，本次收紧并并到 policy |
| `write_file` | 1 KB | true | false | 0 | summary 只回 `"wrote PATH (N bytes)"` |
| `edit_file` | 1 KB | true | false | 0 | summary 只回 `"replaced 1 occurrence in PATH"` |
| `delete_file` | 1 KB | true | false | 0 | summary 只回 `"deleted PATH"` |
| `apply_patch` | 2 KB | true | false | 0 | summary 只回 `"applied patch to N file(s)"` |
| `read_lints` | 32 KB | false | true | 200 | go test 输出可能很长但每行都重要 |
| `semantic_search` | 8 KB | false | true | 0 | 已经 hardcode top-10，policy 只兜底 |
| `ask_clarification` | — | — | — | — | 不经过 policy（middleware 提前拦截） |

`write_file` / `edit_file` 的 `SummaryBuilder` 可读 raw 出来再格式化；最干净
的写法是 tool 本体就返回 `"wrote PATH (N bytes)"` 这种紧凑串，policy 直接拿
来当 summary，**不写 builder**（少压一层）。当前 `write_file` 返回的就是
`"Updated file PATH"`，需要小调成带 byte 数，配合 policy 一步到位。

## 5. 改造影响

### 5.1 新增

- `backend/agent/toolresult/{policy.go, apply.go, table.go}` —— 见 §3
- `backend/agent/middlewares/tool_result_policy.go` —— 见 §3.4

### 5.2 修改

- `backend/agent/middleware_chain.go`：
  - 删 `middlewares.NewToolErrorHandling()`
  - 加 `middlewares.NewToolResultPolicy(emitFn)`，放在 `NewToolCallObservability`
    之后、`patchToolCalls` 之前
- `backend/runtime/eino/events.go`：`ToolResultEvent` 加 `UISummary string`
  字段；`Notify(...)` 暴露 `emitToolUISummary` 给 middleware 注入
- `backend/runtime/eino/deep_runtime.go`：build agent 时把 runtime 的 emit
  callback 传给 `GetChatModelMiddlewares`
- `backend/agent/tools/write_file.go`：返回 `"wrote PATH (N bytes)"`（顺手
  对齐 policy 的 summary 期望，避免 SummaryBuilder）
- `backend/cli/tui/tool_block.go`：
  - **删** `formatArgsLine` / `extractShellCommand` /
    `extractPathSearchArgs` / `extractClarificationArgs` /
    `extractFileWriteArgs` —— 摘要现在从事件里直接拿
  - `extractNewToolBlocks` 改成消费 `ToolResultEvent.UISummary`
  - `renderToolBlock` 保留（折叠展开的人体工学逻辑跟 policy 没关系）
- `backend/agent/middlewares/tool_error.go`：**删整个文件**

### 5.3 `cfg.ToolBlocks.PreviewLines` / `ArgsMaxChars` 的交互

- `PreviewLines` 留着 —— 它管的是 TUI 折叠后展示几行 body，跟 policy 截
  model-facing payload 是两件事。
- `ArgsMaxChars` 留着 —— 它管 TUI header `tool(args)` 那一行，与
  policy 无关。
- 删点：`formatArgsLine` 内部那套 tool-name switch。UISummary 由 policy
  提供，header 就是 `tool_name(UISummary)`，再用 `ArgsMaxChars` 截一次。

## 6. 配置变更

新增 yaml 段（**可选**，留作后续手柄）：

```yaml
tool_result_policy:
  enabled: true
```

默认行为永远开启，加 enabled flag 只是给 rollback 留闸（见 §9）。`MaxBytes`
等具体数字**写死在 Go 代码**：这是工程参数不是用户偏好，扔进 yaml 只会
让两端漂移，违背 AGENTS.md "少传数据"。

**yaml/CHANGELOG.md 要追加的条目**：

```
## 2026-05-14: tool_result_policy section

新增段,在 `tool_blocks` 下面、`models` 上面:

​```yaml
# ============================================================================
# Tool Result Policy
# ============================================================================
# Routes raw tool output through a per-tool policy so the model sees a
# byte-bounded JSON payload (ok/summary/data) and the UI sees a short
# human summary. Per-tool MaxBytes / HeadLines are coded in
# backend/agent/toolresult/table.go (engineering knob, not user pref).
# Set enabled: false to fall back to the old raw-string passthrough
# (kept for one release as a rollback handle).
tool_result_policy:
  enabled: true
​```

驱动:
- `backend/config/yaml.go` / `backend/config/types.go` 加 `Config.ToolResultPolicy`。
- `backend/agent/middleware_chain.go` 根据 enabled 切换 `NewToolResultPolicy`
  vs 旧的 `NewToolErrorHandling`。

背景:`specs/20260514-feature-comparison/step2-tool-result-policy.md`。
```

## 7. 测试计划

- `backend/agent/toolresult/apply_test.go` —— 表驱动 golden test：每个工具
  名一行 `{ rawIn, errIn, wantModelPayload, wantUISummary }`，覆盖：
  - 正常成功（短输出走 IncludeData）
  - 长输出（触发 MaxBytes，回退 summary-only）
  - error 路径（`exec.ExitError`、`os.IsNotExist`、自定义 `fmt.Errorf`）
  - 二进制 / 含 `\x00` 内容（确认不 panic、JSON 能 marshal —— 用 string 转
    json 时 invalid UTF-8 是 issue，要在 `mustJSON` 兜一道 `strings.ToValidUTF8`）
- `backend/agent/middlewares/tool_result_policy_test.go` —— middleware 集成：
  fake endpoint 返回 `(raw, err)`，断言 wrap 后 `(payload, nil)` + emit
  被调用
- `backend/cli/tui/tool_block_test.go` —— 调整 fixture：mock `ToolResultEvent`
  带 `UISummary`，断言渲染消费的是事件字段而非 raw string

## 8. Commit 粒度

按 AGENTS.md "一句话说清一个 diff" 原则拆 4 个 commit：

1. **`toolresult: add policy package`** —— 新建 `backend/agent/toolresult/`
   三个文件 + 测试。零调用方，零行为变更。
2. **`agent: route tool results through policy middleware`** —— 加新
   middleware、改 `middleware_chain.go` 顺序、删 `tool_error.go`、扩
   `events.go`。model 行为开始变。
3. **`tui: consume UISummary from tool result events`** —— `tool_block.go`
   删 `formatArgsLine` 系列、切到事件字段。UI 行为变。
4. **`config: add tool_result_policy flag`** —— yaml schema + 默认值 +
   middleware chain 里的 enabled 分支 + CHANGELOG 一条。

如果 4 太碎，可以把 3+4 并；1 和 2 **不能合**（纯新增 vs 接线，分开 review）。

## 9. 副作用与回滚

### 9.1 行为变化（预期）

- **Model 行为**：所有工具结果从 raw 文本变成 JSON。Model 现在要会读
  `{"ok": true, "summary": ..., "data": ...}`。绝大多数 LLM 见过这种
  shape，但 prompt 里**要加一句**：`<tool_result_format>` 块说明字段
  含义。否则 model 可能把 JSON 当文本继续 grep。
- **Token 用量下降**：长 `execute` / `grep` 是大头，预期削 30~60%。
- **诊断回归**：`execute` 失败时 model 看到的从
  `"...output...\n[Command failed with exit code 1]"` 变成
  `{"ok": false, "summary": "...", "error": "exit code 1"}`。需要 update
  prompt 里关于错误处理的描述。

### 9.2 预期 break 的测试

- `backend/agent/tools/tools_test.go` —— 如果有断言 raw 文本格式的，需要
  改成断言 policy 后的 payload，或把这些测试搬到 `toolresult` 包里直接
  跑 raw（policy 之前）
- `backend/cli/tui/tool_block_test.go` —— 见 §7

### 9.3 Feature flag / 回滚

`tool_result_policy.enabled: false` → middleware chain 装回旧的
`NewToolErrorHandling`，policy 完全旁路。**仅作首版上线一周的安全网**，
一周内确认无回归就删 enabled 开关、删 `tool_error.go` 真删（本 spec 已
计入），spec 闭环。

