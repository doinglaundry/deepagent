# Quickstart: Eino CLI MVP

## Goal

在本地尽快跑通一个可演示的 Eino CLI MVP：可进入 REPL、执行单 Agent 请求、触发基础工具调用、保存会话并恢复最近 checkpoint。

## Recommended Build Order

1. **初始化项目骨架**
   - 创建 `cmd/eino-cli` 和 `internal/` 目录结构
   - 建立配置加载、主入口与基础日志
2. **搭建 REPL 主闭环**
   - 实现输入循环、命令路由、流式渲染、状态展示
   - 先让普通文本与基础 slash command 都可运行
3. **接入 Eino Runtime**
   - 实现单 Agent orchestrator
   - 完成工作区扫描与上下文注入
4. **接入工具系统**
   - 注册最小内置工具：列目录、读文件、受限命令执行
   - 加入权限确认与标准化结果结构
5. **接入状态管理**
   - 保存 session、task、checkpoint
   - 支持中断后恢复最近会话
6. **补齐验证**
   - 为 REPL 主链路、工具确认链路、checkpoint 恢复链路编写测试

## MVP Demo Script

1. 在一个本地仓库启动 CLI
2. 输入一个普通自然语言请求，确认得到流式响应
3. 输入一个 slash command，确认命令路由可用
4. 触发一次工具调用，确认 registry / policy / executor 工作正常
5. 在待确认或处理中断 CLI，再重新进入并恢复最近 checkpoint

## Contract-Driven Checks

- 对照 `contracts/cli-control.openapi.yaml` 检查 session、command、agent run、tool invocation、checkpoint 的输入输出字段
- 在实现前先固定错误码和确认状态枚举，避免模块联调时重复调整

## Out of Scope for This Iteration

- 完整多 Agent 协作
- 完整 MCP / Tool Server 插件发现与远程接入
- 复杂 TUI
- 多模型兼容层的深度适配
