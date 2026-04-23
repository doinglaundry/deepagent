# Tasks: Eino CLI MVP

**Input**: Design documents from `/specs/20260423-eino-cli-mvp/`
**Prerequisites**: `plan.md`, `spec.md`, `research.md`, `data-model.md`, `contracts/`, `quickstart.md`

**Tests**: 未在 feature spec 中显式要求 TDD 或单独测试任务，本任务拆解不生成独立测试任务；每个用户故事保留独立验收标准用于实现后验证。

**Organization**: 任务按用户故事组织，确保每个故事都能独立实现与独立验证。

## Format: `[ID] [P?] [Story] Description`
- **[P]**: 可并行执行（不同文件、无直接依赖）
- **[Story]**: 任务所属故事（`Setup`、`Foundational`、`US1`、`US2`、`US3`、`Polish`）
- 每条任务都包含明确文件路径，要求可以直接执行

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: 初始化 Go CLI 项目骨架和核心目录。

- [x] T001 [Setup] 初始化 Go 模块并创建 CLI 入口 `go.mod`、`cmd/eino-cli/main.go`
- [x] T002 [P] [Setup] 创建 REPL 相关骨架文件 `internal/cli/repl/repl.go`、`internal/cli/router/router.go`、`internal/cli/render/render.go`、`internal/cli/status/status.go`、`internal/cli/taskview/taskview.go`
- [x] T003 [P] [Setup] 创建 runtime 与 orchestrator 骨架文件 `internal/runtime/eino/runtime.go`、`internal/orchestrator/orchestrator.go`、`internal/orchestrator/types.go`
- [x] T004 [P] [Setup] 创建 workspace、tools、session、memory、task、config 骨架文件 `internal/workspace/workspace.go`、`internal/tools/tool.go`、`internal/session/session.go`、`internal/task/task.go`、`internal/memory/store/store.go`、`internal/config/config.go`

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: 所有用户故事都会依赖的基础能力；本阶段未完成前，不开始任何用户故事实现。

- [x] T005 [Foundational] 实现配置加载与本地状态路径解析 `internal/config/config.go`、`internal/config/schema/schema.go`
- [x] T006 [P] [Foundational] 实现工作区识别与基础扫描 `internal/workspace/workspace.go`、`internal/workspace/scan/scan.go`
- [x] T007 [P] [Foundational] 定义共享领域模型与状态枚举 `internal/orchestrator/types.go`、`internal/session/session.go`、`internal/task/task.go`、`internal/tools/tool.go`
- [x] T008 [Foundational] 实现统一错误模型与结果封装，对齐 `specs/20260423-eino-cli-mvp/contracts/cli-control.openapi.yaml`，修改 `internal/runtime/eino/result.go`、`internal/cli/render/types.go`
- [x] T009 [Foundational] 打通应用启动装配链路，在 `cmd/eino-cli/main.go` 中连接 config、workspace、repl 构造流程

**Checkpoint**: Foundation 完成后，用户故事任务才可开始。

---

## Phase 3: User Story 1 - 交互式单 Agent REPL 闭环 (Priority: P1) 🎯 MVP

**Goal**: 让用户在单仓库中启动 CLI，输入自然语言或 slash command 后能得到单 Agent 流式响应和基础状态展示。

**Independent Test**: 在本地仓库启动 CLI，输入一条自然语言请求和一条 slash command，均能得到可见输出；Runtime 失败时会显示可解释错误且 REPL 不退出。

- [x] T010 [P] [US1] 实现输入分类与命令路由 `internal/cli/router/router.go`
- [x] T011 [P] [US1] 实现流式渲染与状态展示 `internal/cli/render/render.go`、`internal/cli/status/status.go`
- [x] T012 [P] [US1] 实现单 Agent runtime 适配与运行状态跟踪 `internal/runtime/eino/runtime.go`、`internal/orchestrator/orchestrator.go`
- [x] T013 [US1] 按契约实现 session 创建、command 提交与 agent run 查询逻辑 `internal/session/session.go`、`internal/orchestrator/session_service.go`
- [x] T014 [US1] 在 `internal/cli/repl/repl.go` 中集成 router、orchestrator、renderer，形成完整 REPL 主循环

**Checkpoint**: User Story 1 可独立演示，达到 MVP 最小可用闭环。

---

## Phase 4: User Story 2 - 基础工具调用与受控执行 (Priority: P2)

**Goal**: 让单 Agent 能调用内置工具，并在高风险操作时进入明确的确认链路。

**Independent Test**: 在 REPL 中触发一次低风险读操作和一次高风险工具调用；前者直接返回标准化结果，后者出现确认提示，用户选择后结果可见。

- [x] T015 [P] [US2] 实现内置工具注册表与基础工具定义 `internal/tools/registry/registry.go`
- [x] T016 [P] [US2] 实现工具执行器与结果标准化 `internal/tools/execute/execute.go`
- [x] T017 [US2] 实现权限策略、确认状态流转与 invocation 模型 `internal/tools/policy/policy.go`、`internal/session/tool_invocation.go`
- [x] T018 [US2] 将 runtime 工具调用桥接到 registry / policy / executor `internal/orchestrator/tool_bridge.go`、`internal/runtime/eino/runtime.go`
- [x] T019 [US2] 在 REPL 中展示确认提示和工具结果 `internal/cli/repl/approval.go`、`internal/cli/render/render.go`

**Checkpoint**: User Story 2 完成后，CLI 已具备最小工程助手能力。

---

## Phase 5: User Story 3 - 会话、任务与恢复基础能力 (Priority: P3)

**Goal**: 让 CLI 保留最近会话、任务状态和 checkpoint，并在中断后恢复继续执行。

**Independent Test**: 启动一轮包含任务进度变化和待确认状态的会话，中断 CLI 后重新进入，可以恢复最近 checkpoint、看到任务列表并继续执行。

- [x] T020 [P] [US3] 实现会话历史持久化与启动加载 `internal/session/store.go`、`internal/session/session.go`
- [x] T021 [P] [US3] 实现 checkpoint 保存与恢复逻辑 `internal/session/checkpoint/store.go`、`internal/session/checkpoint/recover.go`
- [x] T022 [P] [US3] 实现任务规划、任务跟踪与任务列表展示 `internal/task/planner/planner.go`、`internal/task/tracker/tracker.go`、`internal/cli/taskview/taskview.go`
- [x] T023 [P] [US3] 实现项目级轻量记忆的存储、召回与策略 `internal/memory/store/store.go`、`internal/memory/retrieval/retrieval.go`、`internal/memory/policy/policy.go`
- [x] T024 [US3] 实现上下文注入与恢复入口 `internal/session/inject/inject.go`、`internal/cli/repl/resume.go`
- [ ] T025 [US3] 将 checkpoint / task / memory 的写入挂接到 orchestrator 完成链路 `internal/orchestrator/orchestrator.go`、`internal/session/session.go`

**Checkpoint**: User Story 3 完成后，CLI 的持续使用体验可独立验证。

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: 处理跨故事体验、文档与占位扩展点。

- [ ] T026 [Polish] 完善非 happy path UX：处理非仓库启动、配置缺失、模型不可用、插件端点不可达等情况，修改 `internal/cli/render/render.go`、`internal/cli/status/status.go`、`internal/plugin/gateway/gateway.go`
- [ ] T027 [P] [Polish] 回写并校对开发文档，确保实现结果与 `specs/20260423-eino-cli-mvp/quickstart.md`、`specs/20260423-eino-cli-mvp/contracts/cli-control.openapi.yaml` 一致
- [ ] T028 [Polish] 按 `specs/20260423-eino-cli-mvp/quickstart.md` 手工走通 MVP 演示脚本，并修正发现的实现/文档偏差

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 Setup**: 可立即开始
- **Phase 2 Foundational**: 依赖 Setup 完成；阻塞所有用户故事
- **Phase 3 US1**: 依赖 Foundational；建议先做，作为 MVP
- **Phase 4 US2**: 依赖 Foundational；建议在 US1 稳定后进行
- **Phase 5 US3**: 依赖 Foundational；建议在 US1 稳定后进行，可晚于 US2
- **Phase 6 Polish**: 依赖目标用户故事完成

### User Story Dependencies

- **US1 (P1)**: 无其他故事依赖，是首个可交付增量
- **US2 (P2)**: 逻辑上依赖 US1 的 REPL + runtime 主链路
- **US3 (P3)**: 逻辑上依赖 US1 的 session / repl 主链路，但应保持独立验收

### Parallel Opportunities

- Setup 阶段可并行：`T002`、`T003`、`T004`
- Foundational 阶段可并行：`T006`、`T007`
- US1 阶段可并行：`T010`、`T011`、`T012`
- US2 阶段可并行：`T015`、`T016`
- US3 阶段可并行：`T020`、`T021`、`T022`、`T023`
- Polish 阶段可并行：`T027`

---

## Parallel Example: User Story 1

```bash
Task: "实现输入分类与命令路由 internal/cli/router/router.go"
Task: "实现流式渲染与状态展示 internal/cli/render/render.go internal/cli/status/status.go"
Task: "实现单 Agent runtime 适配与运行状态跟踪 internal/runtime/eino/runtime.go internal/orchestrator/orchestrator.go"
```

## Parallel Example: User Story 3

```bash
Task: "实现会话历史持久化与启动加载 internal/session/store.go internal/session/session.go"
Task: "实现 checkpoint 保存与恢复 internal/session/checkpoint/store.go internal/session/checkpoint/recover.go"
Task: "实现任务规划与任务展示 internal/task/planner/planner.go internal/task/tracker/tracker.go internal/cli/taskview/taskview.go"
Task: "实现项目级轻量记忆 internal/memory/store/store.go internal/memory/retrieval/retrieval.go internal/memory/policy/policy.go"
```

---

## Implementation Strategy

### MVP First

1. 完成 Phase 1 Setup
2. 完成 Phase 2 Foundational
3. 完成 Phase 3 US1
4. 立即验证 US1 的独立验收标准；通过后再进入 US2 / US3

### Incremental Delivery

1. Setup + Foundational 完成后，仓库具备可扩展基础
2. 交付 US1，形成首个可演示 MVP
3. 交付 US2，让 CLI 具备最小工具执行能力
4. 交付 US3，让 CLI 具备持续使用与恢复能力
5. 最后完成 Polish，统一体验与文档

## Notes

- 当前项目未提供 `docs/CONSTITUTION.md` 或 `docs/arch/index.md`，本任务拆解遵循 `plan.md` 中的临时 gates 与 MVP 优先原则
- 总体原则：先闭环，再扩展；先单 Agent，再工具与恢复；多 Agent / 插件 / 多模型只保留占位和隔离点
