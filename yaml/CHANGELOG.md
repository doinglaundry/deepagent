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
