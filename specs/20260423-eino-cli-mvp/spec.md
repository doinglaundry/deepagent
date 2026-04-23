# Feature Specification: Eino CLI MVP

**Feature**: `20260423-eino-cli-mvp`
**Created**: 2026-04-23
**Status**: Draft
**Input**: 基于本地脑暴文档继续推进 TTADK specification，默认中文输出，并尽量收敛 MVP。源文档：[`../brainstorm/eino_cli_brainstorm.md`](../brainstorm/eino_cli_brainstorm.md)

## User Scenarios & Testing *(mandatory)*

### User Story 1 - 交互式单 Agent REPL 闭环 (Priority: P1)

作为个人开发者，我希望启动一个类似 Claude CLI 的交互式 REPL，在单仓库工作区中输入自然语言或 slash command 后，系统能够完成输入解析、上下文装配、调用底层 Eino Runtime 执行单 Agent 请求，并以流式方式返回结果、状态与任务进展，从而形成最小可用的 CLI 使用闭环。

该故事覆盖脑暴文档中的 CLI 交互层、Agent Runtime 的最小单 Agent 能力，以及项目感知层的基础扫描能力。MVP 阶段仅要求单 Agent 正常运行，不要求完整多 Agent 编排、复杂失败恢复或多模型兼容抽象。

**Why this priority**: 没有可运行的 REPL 主闭环，后续任务视图、记忆、插件和多 Agent 都无法被真实验证；这是整个产品的最小价值交付面。

**Technical Implementation**:

- 保留并落地以下目录职责：
  - `cmd/`：CLI 主程序入口，负责启动 REPL
  - `internal/cli/repl/`：维护交互循环，接收输入并分发命令或 Agent 请求
  - `internal/cli/router/`：解析 slash command 与普通输入
  - `internal/cli/render/`：渲染 markdown、代码块、表格和状态消息
  - `internal/cli/status/`：展示当前工作区、模型、权限与任务状态
  - `internal/cli/taskview/`：MVP 中至少展示基础任务进度，不要求复杂 TUI
  - `internal/runtime/eino/`：封装 Eino Agent/Runner 的最小调用能力
  - `internal/orchestrator/`：负责单 Agent 请求调度
  - `internal/workspace/`、`internal/workspace/scan/`：识别仓库根、语言栈、工程元信息
  - `internal/config/`、`internal/config/schema/`：加载用户级与项目级配置
- 交互链路需与脑暴文档一致：User Input → REPL → Command Router → Runtime / Task Dispatcher → Stream Renderer。
- 对多 Agent 相关路径 `internal/agent/factory/`、`internal/agent/roles/`、`internal/orchestrator/multiagent/`、`internal/orchestrator/merge/` 仅保留接口预留或轻量骨架，不纳入 MVP 必做范围。
- slash command 在 MVP 中至少支持基础内置命令集合，例如帮助、退出、状态查看；复杂 skill/plugin 命令解析可后续扩展。
- 输出体验必须包含：流式文本响应、错误信息、当前工作区提示、当前模型/运行模式提示、任务执行中的简要状态反馈。
- 工作区能力以单仓库单工作区为边界，只需要完成基础扫描、入口识别、配置文件与依赖文件识别，不要求符号级深度语义索引。
- 产品目标不是只接入 Eino，而是建立接近 Claude CLI 的 REPL 体验，因此状态呈现与确认机制属于 MVP 设计范围。
- 需要显式约束：MVP 暂不追求完整多模型统一抽象，先以单一默认模型链路跑通。

**Independent Test**: 在一个本地仓库中启动 CLI，输入普通自然语言请求与至少一个 slash command，均可得到可见的流式响应或明确错误，并能看到当前工作区与运行状态。

**Acceptance Scenarios**:

1. **Given** 用户位于一个本地 Git 仓库中启动 CLI，**When** 输入普通自然语言请求，**Then** 系统完成工作区识别、调用单 Agent Runtime 并流式返回结果。
2. **Given** 用户已进入 REPL，**When** 输入受支持的 slash command，**Then** 系统经由命令路由执行对应逻辑并输出结构化反馈。
3. **Given** Runtime 执行失败，**When** REPL 收到错误，**Then** 渲染层以终端友好的方式展示错误并保持会话可继续。

---

### User Story 2 - 基础工具调用与受控执行 (Priority: P2)

作为个人开发者，我希望单 Agent 在执行任务时可以调用统一注册的本地工具，并通过受控执行策略管理权限与结果标准化，这样 CLI 才能从“纯对话”升级到“能操作工作区的工程助手”。

该故事覆盖脑暴文档中的工具与协议化插件层，但 MVP 只聚焦内置工具抽象、注册、执行和权限确认，不要求完整 MCP 风格插件生态或复杂外部 Tool Server 管理。

**Why this priority**: 如果没有最小工具链路，CLI 无法完成工程任务；但相比 REPL 主闭环，工具抽象可以在单 Agent 稳定后落地，因此优先级次之。

**Technical Implementation**:

- 保留并落地以下目录职责：
  - `internal/tools/`：定义工具抽象、参数模式、权限等级和执行接口
  - `internal/tools/registry/`：统一注册内置工具
  - `internal/tools/execute/`：执行工具调用、超时控制和结果标准化
  - `internal/tools/policy/`：按风险等级决定是否确认
  - `internal/plugin/gateway/`、`internal/plugin/discovery/`、`internal/plugin/config/`：MVP 中仅保留扩展点，不要求完整插件发现与鉴权流程
- 工具调用链路需遵循脑暴文档：Agent Tool Call → Tool Registry → Approval Policy → Tool Executor / Plugin Gateway → Tool Result。
- MVP 只要求支持少量高频本地工具，例如读取文件、列目录、执行受限命令；工具集合需与权限等级绑定。
- 需要统一的工具结果结构，至少包含执行状态、标准输出/错误输出摘要、是否需要确认、失败原因。
- 确认机制必须接入 CLI 交互体验，而不是隐藏在底层 Runtime 中；高风险工具调用必须可被用户确认或拒绝。
- 插件协议边界在 MVP 阶段需要先收敛数据结构与错误模型，但不要求实现完整远程插件接入。
- 为控制复杂度，MVP 不要求同时支持内置工具与外部工具的全量并发调度。

**Independent Test**: 让 Agent 执行一个需要读取工作区信息的任务，CLI 能触发工具注册、工具执行和结果回传；对于需要确认的调用，用户可以在终端完成确认后继续执行。

**Acceptance Scenarios**:

1. **Given** Agent 需要读取工作区文件，**When** 发起工具调用，**Then** 系统可从 registry 找到工具并返回标准化结果。
2. **Given** Agent 需要执行高风险工具，**When** policy 判定需要确认，**Then** CLI 显示确认提示并在用户同意后继续执行。
3. **Given** 工具执行超时或失败，**When** executor 返回错误，**Then** 上层 Agent 与渲染层都能收到可解释的失败信息。

---

### User Story 3 - 会话、任务与恢复基础能力 (Priority: P3)

作为个人开发者，我希望在持续使用 REPL 时，系统能够保留基础会话历史、任务进展和中断恢复信息，使一次较长的工程会话不会因为输出过长、执行中断或切换问题而完全丢失上下文。

该故事覆盖脑暴文档中的会话、记忆与上下文管理层，以及项目感知、任务追踪层。MVP 只要求基础 session、task、checkpoint 能力，不要求成熟的项目级长期记忆治理、复杂摘要压缩或多 Agent 上下文隔离策略完全成型。

**Why this priority**: 这部分直接影响 CLI 的“可持续使用体验”，但相比主闭环与工具执行，不是第一天必须全部做深，所以放在第三优先级。

**Technical Implementation**:

- 保留并落地以下目录职责：
  - `internal/session/`：管理 session 生命周期与消息记录
  - `internal/session/checkpoint/`：存储中断点、待确认节点与恢复状态
  - `internal/session/inject/`：组装系统提示、历史消息与工作区上下文
  - `internal/session/summary/`：MVP 中只需预留摘要接口或最小压缩策略
  - `internal/task/`、`internal/task/planner/`、`internal/task/tracker/`：定义任务、子任务、依赖与状态，并在 REPL 中展示基础进度
  - `internal/memory/store/`、`internal/memory/retrieval/`、`internal/memory/policy/`：MVP 中聚焦项目级轻量偏好/事实存储，不要求复杂冲突治理
- 会话链路需遵循脑暴文档：Session Start → Context Assembler → Agent Runtime → Transcript / Checkpoint Update → Memory Update。
- 工作区任务链路需遵循脑暴文档：Enter Workspace → Workspace Scanner → Task Planner → Agent / Tool Execution → Execution Tracker。
- 中断恢复至少支持恢复最近一次会话的基本上下文、未完成任务状态和待确认节点。
- 长会话治理要有成本控制意识，但 MVP 可用简单消息裁剪、摘要占位或 checkpoint 恢复代替成熟压缩策略。
- 项目级记忆需要限制范围，避免记忆与上下文治理失控导致模型质量下降；MVP 应先支持少量高价值偏好与关键事实的持久化。

**Independent Test**: 启动一次包含多轮交互和至少一个任务状态变化的会话，中断后重新进入 CLI，可以看到最近会话摘要或恢复点，并继续未完成任务。

**Acceptance Scenarios**:

1. **Given** 用户进行多轮 REPL 对话，**When** 会话消息持续累积，**Then** 系统能够保存会话记录并在后续请求中装配必要上下文。
2. **Given** 某次执行在待确认或处理中断，**When** 用户重新进入 CLI，**Then** 系统能够恢复最近 checkpoint 并提示继续处理。
3. **Given** 任务被拆分为多个步骤，**When** CLI 渲染任务状态，**Then** 用户可以看到任务的基础进度、阻塞和结果。

### Edge Cases

- 用户在非仓库目录启动 CLI 时，系统需要给出清晰提示，并允许以受限模式继续或引导切换目录。
- slash command 与普通自然语言输入歧义时，路由器需要优先依据明确命令前缀判定。
- 流式输出过程中出现工具确认请求时，渲染层需要正确切换到交互确认状态，避免终端输出错乱。
- 工具调用结果过大时，系统需要进行摘要化展示，避免直接淹没 REPL 输出。
- 恢复的 checkpoint 与当前工作区状态不一致时，系统需要提示用户而不是静默复用旧上下文。
- 多 Agent 骨架已存在但未启用时，系统不能误触发未实现的调度链路。
- 默认模型链路不可用时，CLI 需要给出明确失败原因，并提示检查配置，而不是在 REPL 中无响应。
- 插件扩展点已配置但插件端点不可达时，系统需要隔离插件故障，不能阻断内置工具主链路。

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: 系统 MUST 提供一个交互式 REPL 入口，支持个人开发者在单仓库单工作区内持续输入请求并获取响应。
- **FR-002**: 系统 MUST 区分普通自然语言输入与 slash command，并通过统一路由分发到对应处理链路。
- **FR-003**: 系统 MUST 在普通请求路径中装配工作区上下文，并通过 Eino Runtime 执行单 Agent 请求。
- **FR-004**: 系统 MUST 以流式方式渲染文本结果，并支持 markdown、代码块、表格和状态消息的终端展示。
- **FR-005**: 系统 MUST 在 REPL 中展示当前工作区、当前模型或运行模式，以及任务执行中的基础状态反馈。
- **FR-006**: 系统 MUST 在 MVP 阶段支持基础内置 slash command，至少覆盖帮助、退出和状态查看。
- **FR-007**: 系统 MUST 具备单仓库工作区扫描能力，至少识别仓库根、语言栈、入口文件、配置文件和依赖文件。
- **FR-008**: 系统 MUST 定义统一工具抽象、注册机制和执行接口，使 Agent 可以调用内置工具完成工程任务。
- **FR-009**: 系统 MUST 基于权限等级对工具调用执行受控策略，并在高风险调用时向用户发起确认。
- **FR-010**: 系统 MUST 为工具调用返回统一结果结构，至少包含执行状态、输出摘要、失败原因与确认状态。
- **FR-011**: 系统 MUST 在工具执行失败、超时或被拒绝时，将可解释错误传递给上层 Agent 与 CLI 渲染层。
- **FR-012**: 系统 MUST 支持基础会话生命周期管理，保存消息记录并在后续请求中注入必要历史上下文。
- **FR-013**: 系统 MUST 支持最小 checkpoint 能力，记录中断点、待确认节点与最近一次可恢复状态。
- **FR-014**: 系统 MUST 支持任务的基础结构化表示与状态追踪，并在终端展示任务进度、阻塞与结果。
- **FR-015**: 系统 MUST 支持项目级轻量记忆的存储与召回，用于保存少量高价值偏好与关键事实。
- **FR-016**: 系统 MUST 对长会话提供成本可控的上下文治理机制，MVP 可采用裁剪、摘要占位或 checkpoint 恢复等简单策略。
- **FR-017**: 系统 MUST 保留多 Agent、插件网关和多模型抽象的扩展接口，但 MVP 实现中默认仅要求单 Agent 主链路可用。
- **FR-018**: 系统 MUST 避免未启用的多 Agent 或插件扩展点影响主链路稳定性。
- **FR-019**: 用户 MUST 能够在一次中断后重新进入 CLI，并恢复最近一次会话的基础上下文、未完成任务状态或待确认节点。
- **FR-020**: 系统 MUST 提供明确的错误提示与状态反馈，使用户能够理解失败发生在配置、工作区识别、Runtime 调用还是工具执行阶段。

### Key Entities *(include if feature involves data)*

- **Session**: 表示一次 CLI 会话，包含会话标识、消息历史、最近活跃时间、关联工作区和恢复元信息。
- **Workspace**: 表示当前单仓库工作区，包含仓库根路径、语言栈、入口文件、配置文件和依赖清单等扫描结果。
- **Command**: 表示用户在 REPL 中输入的一次命令或请求，包含输入类型、原始内容、路由结果与执行状态。
- **Agent Run**: 表示一次由 orchestrator 发起的单 Agent 执行，包含上下文输入、运行状态、流式输出片段与最终结果。
- **Tool Spec**: 表示一个已注册工具的定义，包含名称、参数模式、风险等级、执行入口与超时策略。
- **Tool Invocation**: 表示一次具体工具调用，包含请求参数、确认状态、执行结果、错误信息与输出摘要。
- **Task**: 表示一次会话中的结构化任务，包含目标、子步骤、依赖、当前状态、阻塞原因与结果摘要。
- **Checkpoint**: 表示会话的可恢复快照，包含最近上下文、待确认动作、未完成任务状态与时间戳。
- **Project Memory**: 表示在项目范围内持久化的偏好或关键事实，包含内容、来源、适用范围和更新时间。

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 新用户在一个本地仓库中可在 5 分钟内完成 CLI 启动、发起首个自然语言请求并看到有效流式响应。
- **SC-002**: 至少 90% 的 MVP 演示场景能够在单 Agent 主链路上成功完成请求解析、上下文装配与结果输出，不因未启用的多 Agent 或插件能力失败。
- **SC-003**: 用户在一次包含工具调用的典型工程任务中，能够清晰感知当前工作区、执行状态、确认动作与失败原因，且不需要阅读源码即可判断系统当前在做什么。
- **SC-004**: 用户在一次中断后的重新进入场景中，能够恢复最近一次会话的基础上下文或未完成任务，并继续完成主要操作，而不是从零开始。
- **SC-005**: MVP 范围内的核心能力边界清晰：单 Agent REPL、基础工具调用、基础会话/任务恢复可演示；多 Agent 编排、完整插件生态和复杂多模型兼容被明确延后，不影响主流程验收。
