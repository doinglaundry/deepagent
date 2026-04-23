# Data Model: Eino CLI MVP

## 1. Session

- **Purpose**: 表示一次 CLI 会话。
- **Core Fields**:
  - `id`: 会话唯一标识
  - `workspace_root`: 关联工作区根目录
  - `started_at`: 启动时间
  - `last_active_at`: 最近活跃时间
  - `message_history`: 历史消息列表
  - `active_task_ids`: 当前活跃任务集合
  - `latest_checkpoint_id`: 最近 checkpoint 标识
- **Relationships**:
  - 1:N `Command`
  - 1:N `AgentRun`
  - 1:N `Task`
  - 1:N `Checkpoint`
- **Validation Rules**:
  - `workspace_root` 必须可定位到当前工作区或显式受限模式
  - `latest_checkpoint_id` 必须引用当前会话下的 checkpoint

## 2. Workspace

- **Purpose**: 表示当前单仓库工作区与扫描结果。
- **Core Fields**:
  - `root_path`
  - `is_git_repo`
  - `language_stack`
  - `entry_files`
  - `config_files`
  - `dependency_files`
  - `scan_timestamp`
- **Relationships**:
  - 1:N `Session`
  - 1:N `ProjectMemory`
- **Validation Rules**:
  - `root_path` 必须存在
  - 扫描结果必须带时间戳，避免误用过期上下文

## 3. Command

- **Purpose**: 表示用户在 REPL 中输入的一次请求。
- **Core Fields**:
  - `id`
  - `session_id`
  - `raw_input`
  - `input_type` (`natural_language` | `slash_command`)
  - `route_target`
  - `status`
  - `created_at`
- **State Transitions**:
  - `received` → `routed` → `running` → `completed|failed|cancelled`

## 4. AgentRun

- **Purpose**: 表示一次单 Agent 调用。
- **Core Fields**:
  - `id`
  - `session_id`
  - `command_id`
  - `context_snapshot`
  - `model_name`
  - `status`
  - `stream_chunks`
  - `final_output`
  - `error_message`
- **State Transitions**:
  - `pending` → `streaming` → `completed|failed`

## 5. ToolSpec

- **Purpose**: 描述一个已注册工具。
- **Core Fields**:
  - `name`
  - `description`
  - `risk_level` (`low` | `medium` | `high`)
  - `input_schema`
  - `timeout_ms`
  - `executor_ref`
- **Validation Rules**:
  - `name` 必须唯一
  - 高风险工具必须声明确认策略

## 6. ToolInvocation

- **Purpose**: 表示一次具体工具调用。
- **Core Fields**:
  - `id`
  - `agent_run_id`
  - `tool_name`
  - `arguments`
  - `approval_status`
  - `execution_status`
  - `stdout_summary`
  - `stderr_summary`
  - `error_message`
- **State Transitions**:
  - `requested` → `awaiting_approval|executing` → `succeeded|failed|rejected|timed_out`

## 7. Task

- **Purpose**: 表示会话中的结构化任务。
- **Core Fields**:
  - `id`
  - `session_id`
  - `title`
  - `description`
  - `status`
  - `step_summaries`
  - `blocked_by`
  - `result_summary`
- **State Transitions**:
  - `pending` → `in_progress` → `completed|blocked|failed`

## 8. Checkpoint

- **Purpose**: 表示最近一次可恢复快照。
- **Core Fields**:
  - `id`
  - `session_id`
  - `context_excerpt`
  - `pending_confirmation`
  - `open_task_ids`
  - `created_at`
- **Validation Rules**:
  - 每个 session 至少保留最近一个 checkpoint
  - 恢复时必须校验当前 workspace 与快照 workspace 是否一致

## 9. ProjectMemory

- **Purpose**: 表示项目级轻量记忆。
- **Core Fields**:
  - `id`
  - `workspace_root`
  - `memory_type`
  - `content`
  - `source`
  - `updated_at`
- **Validation Rules**:
  - 仅保存高价值偏好与关键事实
  - 冲突项写入前需去重或覆盖
