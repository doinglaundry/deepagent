# yaml/config.yaml CHANGELOG

`yaml/config.yaml` 不入 git(里面常含本地 API key / 临时调试值),git 看不到
shape 变化,所以**形状改动必须在这里留一条**:加段、加字段、改名、删字段、
默认值语义变化,都算 shape 改动。

每条记录的最低信息:
1. 日期(yyyy-mm-dd)
2. 一句话说改了什么
3. 完整 yaml 片段,直接 copy 进 config.yaml 即可同步
4. Go 端读它的位置(`cfg.X` 在哪个 commit / 文件被引入)

读到的人:其他机器 / 同事 / 重装环境 / `git stash` 后回不来的自己。

---

## 2026-05-12: tool_observability section

新增段,在 `token_usage` 下面、`models` 上面:

```yaml
# ============================================================================
# Tool Call Observability
# ============================================================================
# When enabled, every tool invocation emits one slog.Debug record with
# name / duration / input size / output size (or error) so you can trace
# which tool was called and how it performed.
# Argument and result CONTENTS are intentionally never logged, only sizes.
# To actually see the records, set log_level: debug above.
tool_observability:
  enabled: true
```

驱动:`backend/agent/middlewares/tool_observability.go` 读 `cfg.ToolObservability.Enabled`。
默认 `true`;关闭时中间件 short-circuit 为 endpoint 透传。要真正看到 Debug 行,
还要把上面的 `log_level` 调到 `debug`(默认 `info`)。

---

## 2026-05-13: max_concurrent_subagents + hitl_tools(砍 RuntimeContext refactor)

新增两段,在 `tool_observability` 下面、`models` 上面:

```yaml
# ============================================================================
# Subagent Concurrency
# ============================================================================
# Hard upper bound on parallel `task` (subagent) tool calls per turn.
# - SubagentLimit middleware truncates beyond this number at runtime.
# - The system prompt advertises this same number to the LLM so its plan
#   matches what we will actually let through. Both ends MUST agree.
# Unset / zero / negative → fallback 5 (defaultMaxConcurrentSubagents).
max_concurrent_subagents: 5

# ============================================================================
# Human-In-The-Loop Tool Gating
# ============================================================================
# List tool names that must pass through agent.ApprovalCallback before
# the tool actually runs. Empty list = HITL middleware is not mounted
# (zero per-call overhead).
# Default approval is a stdin y/N scanner (CLI/batch only). The TUI
# installs its own tea.Msg-routed approver at startup; that path is the
# only one that's safe to leave a destructive tool on this list when
# running interactively.
hitl_tools: []
```

驱动:
- `backend/config/types.go` 加 `Config.MaxConcurrentSubagents int` / `Config.HITLTools []string`。
- `backend/agent/prompt.go` 用 `effectiveMaxSubagents(cfg)` 把数字注入 system prompt(原来写死 5)。
- `backend/agent/middleware_chain.go` `SubagentLimit` middleware 用 `effectiveMaxSubagents(cfg)`,`HITL` middleware 用 `cfg.HITLTools`(原来读 `RuntimeContext.HITLTools`,本次 refactor 砍掉了那条间接路径,搬来 cfg)。

背景:`specs/20260513-cut-runtimecontext/design.md`。
