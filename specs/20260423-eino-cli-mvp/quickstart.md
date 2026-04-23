# Quickstart: Eino CLI MVP

## Goal

在本地仓库中启动一个可演示的 Eino CLI MVP：进入 REPL、执行单 Agent 请求、使用最小内置 slash command / tool、保存会话与 checkpoint，并在重新启动后恢复最近上下文。

## Current Implemented Scope

- CLI 入口为 `cmd/eino-cli/main.go`，应用装配位于 `internal/app/app.go`
- 启动时加载本地状态目录 `.eino-cli/`，包含 `sessions/`、`memory/`、`checkpoints/`
- 仅支持在 Git 仓库内启动；非仓库目录会直接报错
- REPL 支持自然语言输入与 slash command 路由
- Runtime 当前为 `noop-model`，自然语言请求返回占位响应
- 内置工具仅包含 `/read <file>`、`/ls [dir]`、`/shell <command>`
- `/shell` 被标记为高风险操作，会进入 `awaiting_approval`，当前 MVP 只展示拒绝/待确认提示，不真正放行执行
- REPL 内置命令支持 `/help`、`/status`、`/tasks`、`/memory`、`/exit`
- 每次提交会写回 session、checkpoint 与 task memory；自然语言输入会写入项目级 memory
- 启动时若检测到最近 checkpoint，会显示 resume 信息与 memory context
- 状态栏固定展示 `single-agent` 模式，并提示 plugin gateway 在当前 MVP 中不可用

## MVP Demo Script

1. 在一个 Git 仓库根目录执行：
   ```bash
   go run ./cmd/eino-cli
   ```
2. 启动后确认首先看到状态输出，包含当前 workspace、`single-agent` 模式，以及 plugin gateway unavailable warning
3. 输入自然语言请求，例如：
   ```text
   介绍一下当前项目
   ```
   预期看到 `stub response from noop-model`
4. 输入内置命令查看帮助：
   ```text
   /help
   ```
   预期看到当前支持的命令列表
5. 输入低风险工具命令：
   ```text
   /ls
   ```
   或：
   ```text
   /read go.mod
   ```
   预期直接返回目录列表或文件内容
6. 输入高风险工具命令：
   ```text
   /shell pwd
   ```
   预期看到确认/拒绝提示，并以 `tool_error` 呈现当前 MVP 不允许执行的结果
7. 输入一条新的自然语言请求后退出：
   ```text
   /exit
   ```
8. 再次执行 `go run ./cmd/eino-cli`，预期启动时出现 `resume session` 输出，包含最近 `last input`，并在存在 memory 时显示 `memory context`
9. 输入：
   ```text
   /tasks
   ```
   与：
   ```text
   /memory
   ```
   预期分别看到当前任务视图和已保存的项目级 memory

## Contract-Driven Checks

对照 `contracts/cli-control.openapi.yaml`，确认以下契约与实现一致：

- `Session`：字段为 `id`、`workspaceRoot`、`startedAt`、`lastActiveAt`
- `Command`：字段为 `id`、`sessionId`、`rawInput`、`inputType`、`status`、`output`、`errorCode`、`errorMessage`、`createdAt`、`completedAt`
- `CommandAccepted`：返回 `command` 与 `run` 两个对象，而不是扁平 `commandId/runId/status`
- `AgentRun`：包含 `id`、`sessionId`、`commandId`、`modelName`、`status`、`result`、`startedAt`、`invocations`
- `Result`：统一包含 `success`、`code`、`message`、`output`、`needsUser`
- `ToolInvocation`：字段为 `id`、`toolName`、`arguments`、`approvalStatus`、`executionStatus`、`output`、`errorMessage`、`createdAt`
- `Checkpoint`：字段为 `sessionId`、`workspaceRoot`、`lastInput`、`awaitingApproval`、`updatedAt`
- `ResumeSessionResponse`：当前实现对应“启动时自动恢复并渲染消息”的本地行为，数据来源仍对齐 `session`、`checkpoint`、`tasks`
- `Task`：当前最小字段集为 `id`、`title`、`status`

## Manual Validation Notes

- `go test ./...` 应通过，作为当前 MVP 的基础构建验证
- 非 Git 仓库启动属于预期失败路径，应输出 `workspace is not a git repository`
- plugin gateway 不可用属于预期 warning，不阻断主链路
- 当前没有真实模型调用、没有真正的 tool approval 放行、也没有远程 control plane 服务

## Out of Scope for This Iteration

- 多 Agent 协作
- 真正的模型流式输出
- 完整 MCP / Tool Server 插件发现与远程接入
- 真实 approval 交互后的二次执行
- 复杂 TUI
- 多模型兼容层的深度适配
