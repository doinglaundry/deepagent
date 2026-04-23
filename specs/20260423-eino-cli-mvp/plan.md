# Implementation Plan: Eino CLI MVP

**Feature**: `20260423-eino-cli-mvp` | **Date**: 2026-04-23 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/20260423-eino-cli-mvp/spec.md`

## Summary

交付一个面向单仓库本地开发场景的 Eino CLI MVP：先跑通单 Agent REPL 主闭环，再补齐基础工具调用与受控执行，最后接入基础会话、任务和恢复能力。技术策略是采用单进程 Go CLI、文件型本地状态存储、清晰的模块边界和可演示优先的增量实现，明确延后多 Agent、完整插件生态和复杂多模型兼容。

## Technical Context

**Language/Version**: Go 1.22+  
**Primary Dependencies**: Go standard library、Eino Runtime SDK、终端流式输出/样式库（按需选择轻量库）、YAML/JSON 配置解析库  
**Storage**: 本地文件存储（session、checkpoint、memory、config），MVP 不引入外部数据库  
**Testing**: Go `testing`、表驱动单测、关键链路集成测试、contracts 静态校验  
**Target Platform**: macOS/Linux 终端环境  
**Project Type**: 单仓库单二进制 CLI 项目  
**Performance Goals**: 首次进入 REPL ≤ 2s；常规命令解析响应 ≤ 200ms；恢复最近 checkpoint ≤ 1s；典型工具输出在流式模式下持续可见  
**Constraints**: 单机本地运行；MVP 不依赖外部服务编排；不引入复杂 TUI；高风险工具调用必须显式确认；未启用扩展点不能影响主链路  
**Scale/Scope**: 单用户、本地单仓库、3 个核心用户故事、少量内置工具、单 Agent 主链路

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

当前项目未提供 `docs/CONSTITUTION.md` 或 `.ttadk/memory/constitution.md`，本次采用临时 gate：
- **MVP 优先**：只实现 spec 中 P1-P3 的最小闭环，PASS
- **单项目优先**：保持单二进制、单仓库结构，不拆多服务，PASS
- **本地可验证**：每个阶段都能通过 CLI 演示验证，PASS
- **安全受控**：工具权限确认必须在 CLI 层可见，PASS
- **扩展不阻塞主链路**：多 Agent / plugin / 多模型仅保留接口与隔离点，PASS

Phase 1 设计复检结果：以上 gate 仍全部通过，无需复杂度豁免。

## Project Structure

### Documentation (this feature)

```text
specs/20260423-eino-cli-mvp/
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
├── contracts/
│   └── cli-control.openapi.yaml
└── tasks.md
```

### Source Code (repository root)

```text
cmd/
└── eino-cli/

internal/
├── cli/
│   ├── repl/
│   ├── router/
│   ├── render/
│   ├── status/
│   └── taskview/
├── runtime/eino/
├── orchestrator/
├── tools/
│   ├── registry/
│   ├── execute/
│   └── policy/
├── plugin/
│   ├── gateway/
│   ├── discovery/
│   └── config/
├── session/
│   ├── checkpoint/
│   ├── inject/
│   └── summary/
├── memory/
│   ├── store/
│   ├── retrieval/
│   └── policy/
├── task/
│   ├── planner/
│   └── tracker/
├── workspace/
│   ├── scan/
│   └── codebase/
└── config/
    └── schema/

tests/
├── integration/
├── contract/
└── unit/
```

**Structure Decision**: 采用 Go 单项目结构，以 `cmd/` + `internal/` 组织 CLI、runtime、tools、session、workspace 等模块；`tests/` 用于分层验证 REPL 主链路、工具权限链路与恢复链路。

## Complexity Tracking

无已知宪章违规项；当前实现不需要额外复杂度豁免。

## Phase 0: Research & Decisions

1. 确认 MVP 的宿主语言与项目形态：采用 Go 单二进制 CLI，而不是多服务架构。
2. 确认状态存储方案：采用本地文件存储 session/checkpoint/memory/config，而不是数据库。
3. 确认 REPL 实现策略：优先标准输入循环 + 流式渲染，不引入复杂 TUI。
4. 确认工具执行边界：通过统一 tool spec + policy + executor 管理内置工具，高风险操作需显式确认。
5. 确认扩展策略：多 Agent、插件网关、多模型能力仅设计接口边界与隔离点，不进入 MVP 主实现。

## Phase 1: Design & Contracts

1. 将 `Session`、`Workspace`、`Command`、`AgentRun`、`ToolSpec`、`ToolInvocation`、`Task`、`Checkpoint`、`ProjectMemory` 落为数据模型。
2. 用逻辑控制面 OpenAPI 契约稳定 REPL 与 orchestrator / session / tools 之间的输入输出结构。
3. 通过 quickstart 明确开发顺序：初始化 CLI 入口 → 接入单 Agent runtime → 加入工具注册/执行 → 接入 session/checkpoint/task。
4. 在实现前先固定错误模型、确认模型和恢复模型，避免后续接口震荡。

## Implementation Strategy

- **阶段 A**：搭建 `cmd/eino-cli`、`internal/cli/repl`、`internal/cli/router`、`internal/cli/render`，形成可输入可输出的空壳 REPL。
- **阶段 B**：接入 `internal/runtime/eino` 与 `internal/orchestrator`，跑通单 Agent 请求链路。
- **阶段 C**：接入 `internal/tools/*`，实现最小工具注册、执行、确认与结果标准化。
- **阶段 D**：接入 `internal/session/*`、`internal/task/*`、`internal/memory/*`，补齐基础恢复与任务展示。
- **阶段 E**：保留 `internal/plugin/*` 与多 Agent 扩展接口，但仅做隔离和占位，不实现完整能力。

## Risks & Mitigations

- **REPL 体验不足**：优先保障流式输出、错误展示、状态提示三件事，而不是追求复杂 UI。
- **工具权限模型返工**：先在 contracts 中固定确认流与错误流，再落实现。
- **状态治理失控**：MVP 只保存最近会话、最近 checkpoint 和少量项目记忆。
- **扩展点拖慢主线**：所有未启用扩展点必须通过空实现或接口隔离绕开主链路。
