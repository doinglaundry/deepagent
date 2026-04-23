# Research: Eino CLI MVP

## Decision 1: 使用 Go 单二进制 CLI 作为 MVP 宿主

- **Decision**: 采用 Go 1.22+ 单二进制 CLI 结构，代码组织为 `cmd/` + `internal/`。
- **Rationale**: 脑暴文档已经使用 `cmd/`、`internal/cli/`、`internal/runtime/eino/` 等典型 Go 项目布局；单二进制结构最适合本地 REPL MVP，能最小化部署和调试成本。
- **Alternatives considered**:
  - Python CLI：REPL 开发快，但与当前目录规划和后续工程化边界不一致。
  - 多服务拆分：会过早引入 RPC/部署复杂度，不符合 MVP 目标。

## Decision 2: 状态存储采用本地文件而非数据库

- **Decision**: session、checkpoint、memory、config 全部使用本地文件持久化。
- **Rationale**: spec 中的目标是单用户、本地单仓库、单机 CLI；文件存储足以支撑恢复、轻量记忆和配置，不需要额外运维依赖。
- **Alternatives considered**:
  - SQLite：一致性更强，但会提前引入 schema 迁移与封装成本。
  - 远端数据库：超出 MVP 场景边界。

## Decision 3: REPL 采用基础终端循环 + 流式渲染

- **Decision**: 使用标准输入循环、流式文本渲染和轻量状态展示，不引入复杂 TUI。
- **Rationale**: 脑暴文档已强调难点在 REPL 交互、确认机制、状态呈现和工程闭环，而非复杂 UI；MVP 应该优先保证稳定可演示。
- **Alternatives considered**:
  - 完整 TUI 框架：交互更丰富，但会拖慢主闭环交付。
  - 非流式输出：实现更简单，但不符合目标体验。

## Decision 4: 工具系统先做内置工具与权限确认闭环

- **Decision**: 先实现内置工具 registry、executor、policy 与统一结果结构；高风险调用必须走确认链路。
- **Rationale**: 工具能力是 CLI 从对话助手变成工程助手的关键，但外部插件生态并不是 MVP 必需项。
- **Alternatives considered**:
  - 直接接远程插件协议：边界和错误模型不稳定，风险高。
  - 无权限确认：违背 spec 的安全与可控要求。

## Decision 5: 多 Agent、插件、多模型只保留扩展边界

- **Decision**: 在 `internal/orchestrator/multiagent/`、`internal/plugin/*` 和模型抽象层保留接口占位，但默认只启用单 Agent 主链路。
- **Rationale**: 脑暴文档明确指出多 Agent、插件协议和多模型兼容是主要复杂度来源；先冻结接口边界，后续再逐步扩展最稳妥。
- **Alternatives considered**:
  - MVP 同时做完整多 Agent：实现成本和验证成本过高。
  - 完全不预留接口：后续演进时会导致大范围重构。

## Decision 6: 测试策略聚焦三条主链路

- **Decision**: 测试优先覆盖 REPL 主闭环、工具确认链路、checkpoint 恢复链路；单测 + 集成测试 + contract 校验组合使用。
- **Rationale**: 这三条链路直接对应 spec 的 P1/P2/P3 和成功标准，可在不引入过多测试基础设施的前提下验证 MVP 价值。
- **Alternatives considered**:
  - 全量端到端测试优先：投入过大，当前仓库还没有实现代码。
  - 只做单测：无法覆盖流式输出、权限确认和恢复行为。
