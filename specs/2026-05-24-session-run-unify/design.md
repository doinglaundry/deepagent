### Goal

收敛 eino-cli 为 **CLI-only**，业务上只保留一种 **Session**（`session_id = "default_session_id"`）与多种 **Run**。去掉 `thread_id`、Gateway、`deepagent.Router`，以及 Runtime 内进程级 `session-{pid}-{nano}`。

- 路径：`.eino-cli/sessions/default_session_id/runs/`、`rollback/`、`checkpoints/`；用户数据 `.eino-cli/users/<uid>/sessions/default_session_id/user-data/`
- aio HTTP 的 shell 会话字段在 Go 侧改为 `ShellSessionID`，与业务 Session 隔离

### Implementation

1. `consts.DefaultSessionID`；`config.SessionDir` / `SessionRunsDir` / `EnsureSessionDirs`；删除 `ThreadDir`、`RunsDir` 根路径
2. `runtimecontext` 仅 `SessionID`；删除 `ThreadID`
3. 删除 `backend/gateway/*`、`deepagent/router.go`；`main.go` 仅 CLI
4. `runs.Record.SessionID`；store 根为 `SessionRunsDir(sid)`
5. `rollback.Store` 使用 `SessionRollbackDir` + session 级 `fixedRoots`
6. Sandbox `Acquire(sessionID)`；local LRU 字段改名
7. TUI `startStream` ctx 带 `DefaultSessionID`；`Runtime` 去掉 `sessionID` 字段
8. `messages_log` 不再 fallback thread

### Tradeoffs

- **Breaking**：磁盘 `threads/` → `sessions/`；旧 `.eino-cli/runs` 不自动迁移
- **Rollback**：`specs/2026-05-19-cli-history-rollback` 中的 `threads/cli` 路径需按新 layout 理解
- **软回滚**：无开关；**硬回滚**：恢复 gateway/router/thread ctx 与旧 paths
