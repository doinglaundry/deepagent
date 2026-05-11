# LLM 错误处理 — 调研 & LLM 仓技术方案

> 调研对象：`/Users/bytedance/PycharmProjects/deer-flow`（LangChain + LangGraph 栈）
> 落地对象：本仓 `eino-cli`（eino `adk` 栈，prebuilt deep agent）

目标：

1. **调研** deer-flow 的 `LLMErrorHandlingMiddleware`：错误分类、退避重试、熔断器、用户兜底消息四件套是怎么咬合的。
2. **对账** eino 已经自带了哪些零件（`ModelRetryConfig` / `WrapModel` 钩子 / `WillRetryError` 事件），我们到底要"补"什么。
3. **落地方案**：在 LLM 仓里加一个 chatModel wrapper（**不走 middleware**，直接在 `lead_agent.go` 里包一层），尊重 `AGENTS.md`（少 indirection、少压调用栈、struct 只装数据、注释只回答 why）。

> 写实现的人可以直接照 §4 + §6 + §7 走；§1～§3 是为了 review 不必再回 deer-flow 翻源码。

---

## 0. 决策已锁定 / 待确认

| 项 | 倾向决策 | 备注 |
|---|---|---|
| 是否要补这块 | ✅ 要 | deer-flow 的 middleware 里目前 LLM 仓真正缺、且与 CLI 形态无关的就这一项 |
| 落地形态 | **不走 middleware**，直接在 `lead_agent.go` 里把 `chatModel` 包一层 | 1 个 wrapper struct（`errorHandlingModel`），retry/分类/熔断/兜底集中在一个文件。AGENTS.md "少压调用栈"——少一个中间件壳子 |
| Retry 实现 | **wrapper 自带 retry 循环**（**关掉** `deepCfg.ModelRetryConfig`） | 必须自带：直接包 chatModel 意味着我们是 eino wrapper 链最内层；若复用 eino retry 又吞 err，retry 会被"假装成功"骗停（见 §4.0 结构性说明） |
| Backoff 策略 | **指数**（base 1s，cap 8s，对齐 deer-flow） | 不解析 `Retry-After` header——CLI 交互式场景下 cap 截 30s+ 等待意义有限，详见 §4.6 |
| 错误分类 | **mirror deer-flow 的 5 类**：`quota` / `auth` / `transient` / `busy` / `generic` | 文本关键字 + status code + exception type 三路打分。中英文关键字都保留 |
| 失败兜底 | **包成 `*schema.Message`（Assistant 角色）返回，不让 err 冒上去** | 让 deep agent 主循环按"模型回了一句话"正常结束这一轮；deer-flow 也这么干。CLI 直接把 error 抛给用户的体验更糟（红色 trace + 整条会话挂掉） |
| Circuit Breaker | **保留**，**与 retry 同进同退**（一个 `enabled` 总开关） | 三态 closed/half_open/open 与 deer-flow 完全一致；不给"只开 retry 不开 cb"留口子（避免两半错误处理逻辑各自半工作） |
| 配置位置 | **新增 `error_handling:` yaml 段** | 跟 `summarization` / `memory` 同级；默认值在 §5 |
| 重试事件 | **`slog.Warn` 每次重试一行** | 不复用 eino 的 `WillRetryError`（搜过仓库零订阅），用 slog 替代更轻 |
| 默认值 | **`error_handling.enabled: true`** | 一个 master switch；要彻底关掉错误处理（包括 retry）就 `enabled: false` |

---

## 1. 背景与现状

### 1.1 deer-flow 那边长什么样

文件：`backend/packages/harness/deerflow/agents/middlewares/llm_error_handling_middleware.py`

挂在 `wrap_model_call`/`awrap_model_call` 上，378 行。把"LLM 调用挂了"分成四个动作：

1. **`_check_circuit()`**：进入时先看熔断器；OPEN 直接返回固定提示。
2. **`_classify_error(exc)`**：拿到 exc 后用关键字 + status code + exception class 名做分类 → `(retriable: bool, reason: str)`。
3. **重试循环**：retriable 且未到 `retry_max_attempts(=3)` → `time.sleep(backoff)` 再调一次；backoff = `min(base * 2^(attempt-1), cap)`，但**如果响应头里有 `Retry-After[-Ms]` 就尊重它**。
4. **失败兜底**：retry 都用完后，按分类返回不同文案的 `AIMessage(content=...)`；同时 `_record_failure()` 喂熔断器。

熔断器（thread-safe `_circuit_lock`）状态机：

```
closed --(failures >= threshold)--> open
open --(now >= open_until)--> half_open
half_open --probe success--> closed
half_open --probe fail--> open (recovery_timeout 重置)
```

### 1.2 LLM 仓现状

```22:25:backend/agent/lead_agent.go
chatModel, err := buildChatModel(ctx, rt.ModelCfg)
if err != nil {
	return nil, nil, err
}
```

- `chatModel` 直接塞进 `deep.Config.ChatModel`，**没有任何 retry / 错误兜底**。
- `deep.Config.ModelRetryConfig` 字段是 `*adk.ModelRetryConfig`，目前 LLM 仓**没填**——零重试。
- 任何 model error（包括 transient）直接通过 `agentImpl.Run(...)` 的 `*adk.AgentEvent` 流冒到 REPL，会显示成红色错误并打断对话循环。

可以验证：

```bash
$ rg 'ModelRetryConfig|IsRetryAble' backend/
# (no results)
```

---

## 2. deer-flow 中间件分模块拆解

抄思路，不抄代码。

### 2.1 错误分类（`_classify_error`）

输入：异常对象 `exc`。输出：`(retriable, reason)`，reason ∈ `{quota, auth, transient, busy, generic}`。

判定优先级（**前置规则赢**）：

1. **quota 关键字** 命中 `exc.detail` 或 `exc.error_code` → `(False, "quota")`
2. **auth 关键字** 命中 → `(False, "auth")`
3. **exception class 名** ∈ `{APITimeoutError, APIConnectionError, InternalServerError, ReadError, RemoteProtocolError}` → `(True, "transient")`
4. **status code** ∈ `{408, 409, 425, 429, 500, 502, 503, 504}` → `(True, "transient")`
5. **busy 关键字** 命中 → `(True, "busy")`
6. fallthrough → `(False, "generic")`

关键字表（保留中英文）：

```python
_BUSY_PATTERNS = ("server busy", "temporarily unavailable", "try again later", ...,
                  "负载较高", "服务繁忙", "稍后重试", "请稍后重试")
_QUOTA_PATTERNS = ("insufficient_quota", "quota", "billing", "credit", "payment",
                   "余额不足", "超出限额", "额度不足", "欠费")
_AUTH_PATTERNS = ("authentication", "unauthorized", "invalid api key", ...,
                  "无权", "未授权")
```

### 2.2 退避（`_build_retry_delay_ms`）

```python
retry_after = _extract_retry_after_ms(exc)        # 优先 response.headers["Retry-After"]
if retry_after is not None:
    return retry_after
backoff = base * (2 ** (attempt - 1))             # base=1000ms
return min(backoff, cap)                          # cap=8000ms
```

`_extract_retry_after_ms` 同时支持秒数 / 毫秒数 / HTTP date 三种 header 值。

### 2.3 熔断器（`_check_circuit` / `_record_success` / `_record_failure`）

字段（全 lock 保护）：

```python
self._circuit_lock = threading.Lock()
self._circuit_failure_count = 0       # closed 态累计失败
self._circuit_open_until = 0.0        # epoch 秒
self._circuit_state = "closed"        # "closed" | "half_open" | "open"
self._circuit_probe_in_flight = False # half_open 时已放出 probe
```

`_check_circuit()` 返回 True 表示**应该 fast-fail**（不进入实际 LLM 调用）：

- `open` 且 `now < open_until` → True
- `open` 且 `now >= open_until` → 切到 `half_open`，并继续走 half_open 分支
- `half_open` 且 `_circuit_probe_in_flight` → True
- `half_open` 且未放 probe → 标记 probe in-flight，返回 False（让这一次调用过去探一下）
- `closed` → False

`_record_failure()` 在 half_open 时立刻切回 open；在 closed 时累计计数，到 threshold 跳 open。

### 2.4 用户兜底文案（`_build_user_message`）

按分类映射：

| reason | 文案（用户视角） |
|---|---|
| quota | "...rejected the request because the account is out of quota..." |
| auth | "...rejected the request because authentication or access is invalid..." |
| busy / transient | "...temporarily unavailable after multiple retries..." |
| generic | `"LLM request failed: {detail}"` |
| 熔断 OPEN | "...currently unavailable due to continuous failures. Circuit breaker is engaged..." |

---

## 3. eino 这边已经提供了什么

> 本节是路线对比的"备查"。我们最终**没有走 middleware 路线**（即没用 §3.2 的 `WrapModel` hook），也**没有复用 §3.1 的 retry 设施**。`§4.0` 解释了"为什么直接包 chatModel 必然要自带 retry"——读者读完 §3 再到 §4.0，结构性矛盾就清楚了。


### 3.1 `adk.ModelRetryConfig`（重试基础设施）

```97:117:/Users/bytedance/go/pkg/mod/github.com/cloudwego/eino@v0.8.11/adk/retry_chatmodel.go
type ModelRetryConfig struct {
	MaxRetries int
	IsRetryAble func(ctx context.Context, err error) bool
	BackoffFunc func(ctx context.Context, attempt int) time.Duration
}
```

- `IsRetryAble == nil` → 所有错误都 retry。
- `BackoffFunc == nil` → 默认指数 (`base 100ms`, `cap 10s`) + jitter (0~50%)。
- retry 用完，包装为 `*RetryExhaustedError{LastErr, TotalRetries}`，且 `Unwrap() == ErrExceedMaxRetries`。

`deep.Config.ModelRetryConfig` 字段：

```95:151:/Users/bytedance/go/pkg/mod/github.com/cloudwego/eino@v0.8.11/adk/prebuilt/deep/deep.go
ModelRetryConfig *adk.ModelRetryConfig
...
ModelRetryConfig: cfg.ModelRetryConfig,
```

把它填上即可启用重试。

### 3.2 `WrapModel` hook

```189:199:/Users/bytedance/go/pkg/mod/github.com/cloudwego/eino@v0.8.11/adk/handler.go
// WrapModel wraps a chat model with custom behavior.
// Return the input model unchanged and nil error if no wrapping is needed.
//
// This method is called at request time when the model is about to be invoked.
// Note: The parameter is BaseChatModel (not ToolCallingChatModel) because wrappers
// only need to intercept Generate/Stream calls. Tool binding (WithTools) is handled
// separately by the framework and does not flow through user wrappers.
...
WrapModel(ctx context.Context, m model.BaseChatModel, mc *ModelContext) (model.BaseChatModel, error)
```

返回的 `model.BaseChatModel` 在 ctx 中替换 inner，**自动同时拦截 Generate 和 Stream**。

### 3.3 Wrapper 链层次（这点决定了 hook 选择）

`wrappers.go` 拼装顺序（外 → 内）：

```
用户 handler.WrapModel (逆序: chain[0] 最外, chain[N-1] 最内)
  → eventSender (eino 默认插入)
    → retryWrapper (modelRetryConfig != nil 时)
      → 真实 model
```

```552:558:/Users/bytedance/go/pkg/mod/github.com/cloudwego/eino@v0.8.11/adk/wrappers.go
if w.modelRetryConfig != nil {
	innerEndpoint := endpoint
	endpoint = func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
		retryWrapper := newRetryModelWrapper(&endpointModel{generate: innerEndpoint}, w.modelRetryConfig)
		return retryWrapper.Generate(ctx, input, opts...)
	}
}
```

**关键结论**：用户 `WrapModel` 在 retry **外层**——这是 middleware 路线下"能接住 RetryExhaustedError"的根据。**反过来**也意味着：如果不走 middleware、改在 `lead_agent.go` 里直接包 chatModel，我们的 wrapper 会落在 eino retry **内层**——这就是 §4.0 那个结构性矛盾的来源。

### 3.4 `WillRetryError` 事件

```68:91:/Users/bytedance/go/pkg/mod/github.com/cloudwego/eino@v0.8.11/adk/retry_chatmodel.go
// WillRetryError is emitted when a retryable error occurs and a retry will be attempted.
// It allows end-users to observe retry events in real-time via AgentEvent.
type WillRetryError struct {
	ErrStr       string
	RetryAttempt int
	err          error
}
```

eino retry 包装出来的 err 走的是 `WithErrWrapper`，会发到 `AgentEvent`。TUI 想观察"正在重试"，订阅 event 流即可，**不需要我们自己 emit**。

---

## 4. 落地方案

### 4.0 结构性前提：为什么 retry 必须自带

把 wrapper 直接放在 `lead_agent.go` 里包 chatModel，我们在 eino wrapper 链里的位置是**最内层**（就是"actual model"那个位置）。链层次（外→内）：

```
eino retry → eventSender → 我们的 wrapper（=inner） → 真实 chat model
```

**如果仍然复用 eino retry**（`deepCfg.ModelRetryConfig` 非空），同时 wrapper 又把 err 吃掉返回 `(fallbackMsg, nil)`：

- eino retry 调我们的 `Generate`
- 我们吞 err、返回 fallback msg
- eino retry 看到 `err == nil` → **直接结束 retry**

retry 被"假装成功"骗停，等于失效。反过来如果让 wrapper 透传 err：那 `*RetryExhaustedError` 最终冒出 eino retry 时，外面没有任何人接得住——直接到 REPL 红色错误。

**结论**：直接包 chatModel 这条路 ⇒ 必须 `ModelRetryConfig = nil`，retry 由 wrapper 自己在 Generate 内部跑循环。

### 4.1 文件清单

| 文件 | 角色 |
|---|---|
| `backend/agent/error_handling.go` | wrapper 本体（`errorHandlingModel` + 顶层函数：分类、backoff、兜底、熔断） |
| `backend/agent/error_handling_test.go` | 分类、retry、backoff、熔断、兜底测试 |
| `backend/agent/lead_agent.go` | 一行：`chatModel = wrapErrorHandling(chatModel, cfg.ErrorHandling)` |
| `backend/config/yaml.go` + `backend/config/types.go` | 新增 `ErrorHandling` 段 |
| `yaml/config.yaml` | 默认配置项 |

注意：**不在** `middlewares/` 目录下——这是一个 chatModel wrapper，不是 middleware。`middleware_chain.go` 不动。

### 4.2 数据结构

按 AGENTS.md "结构体只装数据"：

```go
// Package agent
//
// errorHandlingModel wraps a BaseChatModel to:
//   - classify transport errors into 5 buckets (quota/auth/transient/busy/generic)
//   - retry transient/busy with exponential backoff (capped)
//   - surface terminal failures as an Assistant message (so the deep agent
//     loop ends gracefully instead of bubbling err up to abort the run)
//   - fast-fail via a circuit breaker after N consecutive failures
//
// Mirrors deer-flow's LLMErrorHandlingMiddleware in shape.
// The whole feature is gated by config.ErrorHandling.Enabled; when disabled
// wrapErrorHandling returns the inner model unchanged. Both retry and cb
// are always present when this struct exists — no per-feature nil checks.
type errorHandlingModel struct {
	inner model.BaseChatModel
	retry retryConfig
	cb    *circuitBreaker
}

type retryConfig struct {
	maxAttempts int           // always >= 1 when this struct is alive
	baseDelay   time.Duration // first backoff
	capDelay    time.Duration // cap on exponential growth
}

type circuitBreaker struct {
	threshold int
	recovery  time.Duration
	logger    *slog.Logger

	mu            sync.Mutex
	state         circuitState
	failures      int
	openUntil     time.Time
	probeInFlight bool
}

type circuitState string

const (
	circuitClosed   circuitState = "closed"
	circuitHalfOpen circuitState = "half_open"
	circuitOpen     circuitState = "open"
)
```

`retryConfig` 是值类型（无锁，构造后只读）；`*circuitBreaker` 用指针因为带可变状态 + 锁。两者**总是同时存在**——禁用走 `wrapErrorHandling` 返回 inner 这条路，让 `errorHandlingModel.Generate` 内部不再写 `if e.cb != nil` / `if e.retry.enabled` 这种"半工作"分支。AGENTS.md "结构体只装数据"。

### 4.3 构造：`wrapErrorHandling`

```go
// wrapErrorHandling — single integration point. lead_agent.go calls this on
// the freshly built chatModel and passes the result to deep.Config.ChatModel.
// Returns inner unchanged when the feature is disabled, so callers don't pay
// for an extra layer they didn't ask for.
//
// Retry + circuit breaker are a single feature — flipping one without the
// other would split error-handling into two halves that need to agree on
// every error class. We expose one master switch instead.
func wrapErrorHandling(inner model.BaseChatModel, cfg config.ErrorHandling) model.BaseChatModel {
	if !cfg.Enabled || cfg.Retry.MaxAttempts <= 0 {
		return inner
	}
	return &errorHandlingModel{
		inner: inner,
		retry: retryConfig{
			maxAttempts: cfg.Retry.MaxAttempts,
			baseDelay:   time.Duration(cfg.Retry.BaseDelayMS) * time.Millisecond,
			capDelay:    time.Duration(cfg.Retry.CapDelayMS) * time.Millisecond,
		},
		cb: &circuitBreaker{
			threshold: cfg.CircuitBreaker.FailureThreshold,
			recovery:  time.Duration(cfg.CircuitBreaker.RecoverySeconds) * time.Second,
			logger:    slog.Default(),
			state:     circuitClosed,
		},
	}
}
```

签名只吃 `config.ErrorHandling` 一个参数（AGENTS.md "少传数据"）。
`cfg.Retry.MaxAttempts <= 0` 是配置正确性兜底——不是给"想关 retry 但留 cb"留逃生口。

### 4.4 `Generate` / `Stream`：retry 循环 + 兜底

```go
func (e *errorHandlingModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	if e.cb.shouldFastFail() {
		return circuitOpenMessage(), nil
	}

	var lastErr error
	var lastReason errorReason
	for attempt := 1; attempt <= e.retry.maxAttempts; attempt++ {
		out, err := e.inner.Generate(ctx, input, opts...)
		if err == nil {
			e.cb.recordSuccess()
			return out, nil
		}
		reason := classifyError(err)
		lastErr, lastReason = err, reason

		retryable := reason == reasonTransient || reason == reasonBusy
		if !retryable || attempt == e.retry.maxAttempts {
			break
		}

		sleepDuration := getBackoffDuration(e.retry, attempt)
		slog.Warn("LLM call failed; will retry",
			"reason", reason, "attempt", attempt, "max", e.retry.maxAttempts,
			"sleep_ms", sleepDuration.Milliseconds(), "err", err)
		if err := sleepCtx(ctx, sleepDuration); err != nil {
			lastErr = err
			break
		}
	}

	e.cb.recordFailure()
	slog.Warn("LLM call failed; surfacing fallback assistant message",
		"reason", lastReason, "err", lastErr)
	return fallbackMessage(lastReason, lastErr), nil
}

// Stream 同构: 失败时返回单元素 StreamReader 包 fallback。
func (e *errorHandlingModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	out, _ := e.Generate(ctx, input, opts...) // 复用 Generate 路径——简单且与 deer-flow 行为一致
	r, w := schema.Pipe[*schema.Message](1)
	w.Send(out, nil)
	w.Close()
	return r, nil
}

// sleepCtx — backoff 期间也尊重 ctx 取消(用户 Ctrl-C 不应再等 8s)。
func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
```

⚠️ **关键设计点**：返回 `Role: Assistant` 的消息，**ToolCalls 为空**。这样 deep agent 主循环看到一条"模型回了一句话、没有 tool calls" → 结束本轮 `Run()`，REPL 拿到这条消息当成普通回复展示。**不会**让 deep agent 反复 retry 这条假消息。

> Stream 路径偷懒：直接 fallthrough 到 Generate 再单元素 pipe 化。CLI 当前是阻塞调用，stream 用例少；后续真要用 stream 再独立实现，当前先保正确性。

### 4.5 错误分类（顶层函数，无 receiver）

```go
type errorReason string

const (
	reasonQuota     errorReason = "quota"
	reasonAuth      errorReason = "auth"
	reasonTransient errorReason = "transient"
	reasonBusy      errorReason = "busy"
	reasonGeneric   errorReason = "generic"
)

// Order matters: quota/auth checks come first because quota/auth keywords
// may co-occur with transient phrasings (e.g. "429 quota exceeded").
func classifyError(err error) errorReason {
	if err == nil {
		return reasonGeneric
	}
	detail := strings.ToLower(err.Error())
	switch {
	case matchesAny(detail, quotaPatterns):
		return reasonQuota
	case matchesAny(detail, authPatterns):
		return reasonAuth
	case isTransientStatusOrException(detail):
		return reasonTransient
	case matchesAny(detail, busyPatterns):
		return reasonBusy
	}
	return reasonGeneric
}

var (
	busyPatterns  = []string{"server busy", "temporarily unavailable", "try again later", "please retry", "please try again", "overloaded", "high demand", "rate limit", "负载较高", "服务繁忙", "稍后重试", "请稍后重试"}
	quotaPatterns = []string{"insufficient_quota", "quota", "billing", "credit", "payment", "余额不足", "超出限额", "额度不足", "欠费"}
	authPatterns  = []string{"authentication", "unauthorized", "invalid api key", "invalid_api_key", "permission", "forbidden", "access denied", "无权", "未授权"}
)

// isTransientStatusOrException covers two providers' worth of patterns:
//  1. HTTP status code in the URL/message ("status 503" / "HTTP 429 ...")
//  2. typical SDK exception names embedded in err.Error()
//     (eino's chat-model adapters wrap raw provider exceptions into Go errors
//     whose .Error() preserves the original exception class name).
func isTransientStatusOrException(detail string) bool {
	for _, code := range []string{"408", "409", "425", "429", "500", "502", "503", "504"} {
		if strings.Contains(detail, code) {
			return true
		}
	}
	for _, name := range []string{"apitimeouterror", "apiconnectionerror", "internalservererror", "readerror", "remoteprotocolerror", "context deadline exceeded", "connection reset", "i/o timeout"} {
		if strings.Contains(detail, name) {
			return true
		}
	}
	return false
}

func matchesAny(haystack string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}
```

> **说明**：Python 那边可以 `exc.__class__.__name__` 直接拿，Go 这边没有等价物（error 是值），所以退化为"在 err.Error() 字符串里找 SDK 类名子串"。eino 的几个 provider 适配器（kimi、qwen、doubao、openai-compatible）都把 SDK 错误名包进 `Error()`——已抽样验证。

### 4.6 Backoff

```go
// getBackoffDuration — sleep duration before the next retry attempt.
// attempt is 1-based (first retry = attempt 1).
func getBackoffDuration(cfg retryConfig, attempt int) time.Duration {
	exp := cfg.baseDelay << (attempt - 1)
	return min(exp, cfg.capDelay)
}
```

> **不解析 `Retry-After` header**——LLM 仓是单用户交互式 CLI。`Retry-After` 通常出现在 429 速率限制场景，其值（30s~60s）远超 cap（8s），即使解析出来也会被截到 cap；同时用户看到 fallback 消息后停几秒手动重发就好。指数退避足以覆盖瞬时抖动 + 短暂忙这两类真正可以靠重试救回的失败。

### 4.7 兜底消息（顶层函数）

```go
func fallbackMessage(reason errorReason, err error) *schema.Message {
	return &schema.Message{
		Role:    schema.Assistant,
		Content: fallbackText(reason, err),
	}
}

func fallbackText(reason errorReason, err error) string {
	switch reason {
	case reasonQuota:
		return "LLM provider rejected the request: account quota / billing problem. Please check the provider account and try again."
	case reasonAuth:
		return "LLM provider rejected the request: authentication or access is invalid. Please check the provider credentials and try again."
	case reasonBusy, reasonTransient:
		return "LLM provider is temporarily unavailable after multiple retries. Please wait a moment and continue the conversation."
	}
	if err != nil {
		return fmt.Sprintf("LLM request failed: %s", err.Error())
	}
	return "LLM request failed."
}

func circuitOpenMessage() *schema.Message {
	return &schema.Message{
		Role:    schema.Assistant,
		Content: "LLM provider is currently unavailable due to continuous failures. Circuit breaker is engaged to protect the system. Please wait a moment before trying again.",
	}
}
```

### 4.8 熔断器（方法挂在 `*circuitBreaker`）

熔断器是有状态对象（state machine + mutex），方法挂在 `*circuitBreaker` 上。`Generate` 里直接 `e.cb.shouldFastFail()` / `e.cb.recordSuccess()` / `e.cb.recordFailure()`——cb 始终非 nil（见 §4.2/§4.3），不需要任何 nil 包装。

> 所有失败 reason 都计入熔断（含 `quota` / `auth`），对齐 deer-flow 行为：鉴权 / 配额错误持续出现时，熔断打开正好暴露"上游账号 / 密钥配置炸了"这种持久性故障，不应被特殊忽略。

```go
func (cb *circuitBreaker) shouldFastFail() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	now := time.Now()

	if cb.state == circuitOpen {
		if now.Before(cb.openUntil) {
			return true
		}
		cb.state = circuitHalfOpen
		cb.probeInFlight = false
	}
	if cb.state == circuitHalfOpen {
		if cb.probeInFlight {
			return true
		}
		cb.probeInFlight = true
		return false
	}
	return false
}

func (cb *circuitBreaker) recordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.state != circuitClosed || cb.failures > 0 {
		cb.logger.Info("circuit breaker reset (closed)")
	}
	cb.failures = 0
	cb.openUntil = time.Time{}
	cb.state = circuitClosed
	cb.probeInFlight = false
}

func (cb *circuitBreaker) recordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.state == circuitHalfOpen {
		cb.openUntil = time.Now().Add(cb.recovery)
		cb.state = circuitOpen
		cb.probeInFlight = false
		cb.logger.Warn("circuit breaker probe failed (open)",
			"recovery_seconds", int(cb.recovery.Seconds()))
		return
	}
	cb.failures++
	if cb.failures >= cb.threshold && cb.state != circuitOpen {
		cb.openUntil = time.Now().Add(cb.recovery)
		cb.state = circuitOpen
		cb.probeInFlight = false
		cb.logger.Warn("circuit breaker tripped (open)",
			"threshold", cb.threshold,
			"recovery_seconds", int(cb.recovery.Seconds()))
	}
}
```

> deer-flow 在 `quota`/`auth` 上**会**计入熔断，我们故意调整为**不计入**——见 §4.8 顶端"过滤策略与熔断机制分层"的说明。

### 4.9 `lead_agent.go` 接入

唯一接入点，一行：

```go
func MakeLeadAgent(ctx context.Context, rt *RuntimeContext, cfg *config.Config) (adk.ResumableAgent, *middlewares.Trace, error) {
	chatModel, err := buildChatModel(ctx, rt.ModelCfg)
	if err != nil {
		return nil, nil, err
	}
	chatModel = wrapErrorHandling(chatModel, cfg.ErrorHandling) // ← 新增

	// ...（其余原样保留 deepCfg/Handlers）
	deepCfg := &deep.Config{
		Name:        rt.AgentName,
		Description: "Deep Agent",
		ChatModel:   chatModel,
		// ModelRetryConfig: 不设 — 由 wrapErrorHandling 内部自带 retry,
		//   见 §4.0 结构性说明。
		// ...
	}
	// ...
}
```

`middleware_chain.go` **不动**。`buildModelRetryConfig` 之类的 helper **不存在**。

### 4.10 与现有 `tool_error.go` 的关系

`middlewares.ToolErrorHandling.WrapInvokableToolCall` 把 tool **执行**错误转成 ToolMessage；`errorHandlingModel` 处理 chat **model** 错误。两者作用面不重叠，可以共存。

---

## 5. 配置（yaml）

```yaml
# yaml/config.yaml (新增,放在 summarization: 段附近)
error_handling:
  enabled: true            # 总开关 — false 时整个 wrapper 不挂,LLM err 原样冒
  retry:
    max_attempts: 3
    base_delay_ms: 1000
    cap_delay_ms: 8000
  circuit_breaker:
    failure_threshold: 5
    recovery_seconds: 60
```

> 设计上**故意不提供** `retry.enabled` / `circuit_breaker.enabled` 这种子开关——retry 和熔断器是一体两面（一个负责"抖动时多试几次"、一个负责"持续挂时别瞎试"），分开开关只会造出"半工作"的边界 case。要彻底关掉错误处理：`error_handling.enabled: false`。

types.go / yaml.go 新增：

```go
type ErrorHandling struct {
	Enabled        bool                        `yaml:"enabled"`
	Retry          ErrorHandlingRetry          `yaml:"retry"`
	CircuitBreaker ErrorHandlingCircuitBreaker `yaml:"circuit_breaker"`
}

type ErrorHandlingRetry struct {
	MaxAttempts int `yaml:"max_attempts"`
	BaseDelayMS int `yaml:"base_delay_ms"`
	CapDelayMS  int `yaml:"cap_delay_ms"`
}

type ErrorHandlingCircuitBreaker struct {
	FailureThreshold int `yaml:"failure_threshold"`
	RecoverySeconds  int `yaml:"recovery_seconds"`
}

// Config 上加:
//   ErrorHandling ErrorHandling `json:"-" yaml:"error_handling"`
```

默认值 fallback（在读完 yaml 之后 normalize）：

| 字段 | 缺省 | 与 deer-flow 对照 |
|---|---|---|
| `enabled` | `true` | deer-flow 没有总开关，分散在多处；我们合一 |
| `retry.max_attempts` | `3` | `retry_max_attempts=3` |
| `retry.base_delay_ms` | `1000` | `retry_base_delay_ms=1000` |
| `retry.cap_delay_ms` | `8000` | `retry_cap_delay_ms=8000` |
| `circuit_breaker.failure_threshold` | `5` | `circuit_failure_threshold=5` |
| `circuit_breaker.recovery_seconds` | `60` | `circuit_recovery_timeout_sec=60` |

---

## 6. 测试计划

`backend/agent/error_handling_test.go`：

**分类（纯函数，表驱动）**

| 用例 | 输入 | 期望 |
|---|---|---|
| `TestClassifyError_QuotaKeyword` | `"Insufficient quota"` | `reasonQuota` |
| `TestClassifyError_AuthKeyword_Chinese` | `"未授权访问"` | `reasonAuth` |
| `TestClassifyError_StatusCode503` | `"upstream returned 503 service unavailable"` | `reasonTransient` |
| `TestClassifyError_BusyKeyword` | `"服务繁忙，请稍后重试"` | `reasonBusy` |
| `TestClassifyError_QuotaBeats429` | `"429 insufficient_quota"` | `reasonQuota`（quota 优先级高于 status code） |
| `TestClassifyError_Generic` | `"weird thing"` | `reasonGeneric` |
| `TestClassifyError_Nil` | `nil` | `reasonGeneric` |

**Backoff**

| 用例 | 验证点 |
|---|---|
| `TestGetBackoffDuration_ExponentialClamped` | `attempt=1/2/3/4` 在 `base=1000`, `cap=8000` 下应为 `1000/2000/4000/8000` |

**Retry 循环**

| 用例 | 验证点 |
|---|---|
| `TestGenerate_SuccessFirstTry` | inner 一次成功 → 返回 inner msg，调用次数=1 |
| `TestGenerate_TransientThenSuccess` | 1 次 503 + 1 次成功 → 返回 inner msg，调用次数=2 |
| `TestGenerate_TransientExhausted` | 全部 503，max_attempts=3 → 返回 fallback（reason=transient），调用次数=3 |
| `TestGenerate_NonRetryableNoRetry` | 一次 quota err → 不重试，返回 fallback，调用次数=1 |
| `TestGenerate_CtxCanceledDuringBackoff` | retry 间隙 ctx.Cancel() → 立刻返回 fallback，不再等满 backoff |

**兜底消息形状**

| 用例 | 验证点 |
|---|---|
| `TestFallbackMessage_AssistantRole_NoToolCalls` | role=Assistant, ToolCalls==nil（保证 deep agent 结束本轮） |
| `TestFallbackText_QuotaContainsCue` | quota 文案含 "quota" 关键字让用户能识别 |

**Circuit breaker（方法在 `*circuitBreaker`，可独立测）**

| 用例 | 验证点 |
|---|---|
| `TestRecordFailure_ClosedToOpen` | 连续 N 次 `recordFailure()` 后 `shouldFastFail()` 返回 true |
| `TestRecordFailure_HalfOpenProbeFailReopens` | half_open 探针失败 → 立刻 open + 重置 openUntil |
| `TestRecordSuccess_ResetsCounter` | failures 清零、state 回 closed |
| `TestShouldFastFail_HalfOpenProbeInFlight` | half_open 放一个 probe，第二次 check 必然 fast-fail |
| `TestGenerate_QuotaDoesNotTripCircuit` | quota err 重复 N+1 次后 cb 仍 closed（过滤在 Generate 里） |

**集成**

| 用例 | 验证点 |
|---|---|
| `TestWrapErrorHandling_DisabledReturnsInner` | `cfg.Enabled=false` → 返回的 BaseChatModel 是 inner 本身（同一指针） |
| `TestWrapErrorHandling_MaxAttemptsZeroReturnsInner` | `cfg.Retry.MaxAttempts=0` → 同上（配置兜底） |
| `TestGenerate_CircuitOpenSkipsModel` | cb 已 open → Generate 不调 inner，直接返回 fallback |

伪 inner 用 `model.BaseChatModel` 接口实现一个 `func`-based stub（按调用次数返回不同 err/msg）。

---

## 7. 不做的事（按 AGENTS.md 外科手术化）

- ❌ **复用 eino `ModelRetryConfig`**：结构性冲突（见 §4.0）；我们在 lead_agent 里直接包 chatModel，自己跑 retry 循环。
- ❌ **emit `WillRetryError` 等 AgentEvent**：搜过仓库零订阅，slog.Warn 每次重试一行更轻；将来 TUI 需要"重试进度条"再补。
- ❌ **解析 `Retry-After[-Ms]` header**：单用户交互式 CLI 场景下，`Retry-After` 值（30s+）通常远超 cap（8s），即使解析出来也会被截到 cap；fallback 消息提示用户手动重发足够，不值得多 20 行代码 + 字符串扫描。详见 §4.6。
- ❌ **Stream 模式逐 chunk 分类**：当前 LLM 仓 REPL 是阻塞调用，stream 用例少；Stream 直接 fallthrough 到 Generate 包成单元素 StreamReader。
- ❌ **熔断器全局共享**：每个 wrapper 实例独立计数；同一个 agent 在不同会话/不同 model 配置下熔断状态各算各的。如果未来要"按 provider 维度共享"，再补 store。
- ❌ **deer-flow 的 `GraphBubbleUp` 等价物**：eino 没有 LangGraph 的中断/暂停语义，不需要这层判断。

---

## 8. 实施步骤（可验证）

1. **加 yaml schema + 默认值**（`types.go` + `yaml.go` + `yaml/config.yaml`）→ 验证：`go test ./backend/config/...` 通过；启动 CLI 不读 config 段也能跑（零值=全 disabled，向后兼容）。
2. **写 `classifyError` + patterns 表 + 表驱动单测** → 验证：§6 分类用例全绿。
3. **写 `getBackoffDuration` + 单测** → 验证：§6 backoff 用例全绿。
4. **写 `*circuitBreaker` + 单测** → 验证：§6 熔断 6 条用例全绿。
5. **写 `errorHandlingModel` + `wrapErrorHandling` + retry 循环 + 集成单测** → 验证：§6 集成 + retry 循环用例全绿。
6. **接入 `lead_agent.go` 一行 `chatModel = wrapErrorHandling(...)`** → 验证：`go build ./backend/agent/...` 通过；`lead_agent_test` 不退化；启动 CLI 与真实 provider 跑一轮普通对话。
7. **手工冒烟**：临时把 API key 改错 → 应该看到一条 Assistant 兜底消息（包含 "authentication or access is invalid"）而不是红色 trace；恢复 key 后下一轮正常。

每步独立可 commit；建议 split 成 6~7 个 small PR-sized commit（参考 AGENTS.md "纯重命名 ≠ 行为变更"）。

---

## 9. 与 deer-flow 的差异收尾

| 维度 | deer-flow | LLM 仓（本方案） | 备注 |
|---|---|---|---|
| 集成形态 | `AgentMiddleware`（`wrap_model_call`） | chatModel wrapper（lead_agent 一行接入） | 不走 middleware；调用栈少一层 |
| Retry 循环 | 自己写 | 自己写（§4.4） | wrapper 内闭环；eino retry 关掉 |
| Backoff | `base * 2^(n-1)`，cap | 同 | 行为对齐 |
| Retry-After header | 支持（seconds/ms/HTTP date） | 不支持（纯指数 cap） | CLI 交互式场景下 cap 截 30s+ 等待意义有限，详见 §4.6 / §7 |
| 错误分类 | 5 类 | 5 类 | 关键字表完全对齐（含中文） |
| Circuit breaker | 独立 yaml 段，默认开 | 与 retry 同步开关（一个 master switch），默认开 | 不让两个开关错配出"半工作"边界 case |
| Quota/Auth 进熔断 | 进 | 进 | 对齐 deer-flow，理由见 §4.8 |
| 失败兜底 | `AIMessage` | `*schema.Message{Role: Assistant, ToolCalls: nil}` | 等价 |
| 重试事件 | `langgraph stream writer` emit `llm_retry` | `slog.Warn` 每次重试一行 | LLM 仓零 AgentEvent 订阅，slog 替代足够 |
| 并发安全 | `threading.Lock` | `sync.Mutex`（仅 `*circuitBreaker` 内部） | 同 |

---

## 10. 已敲定决策

1. **`quota` / `auth` 错误计入熔断**——对齐 deer-flow。密钥 / 配额持续失败时让熔断打开，正好暴露持久性配置故障，不做特殊豁免。
2. **`fallbackText` 用英文**——与现有 middlewares 风格保持一致。
3. **wrapper 文件归属：`backend/agent/error_handling.go`**——它不是 `adk.ChatModelAgentMiddleware`，放进 `middlewares/` 会误导后来人；直接挂在 agent 包根。

