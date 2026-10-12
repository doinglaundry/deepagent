# 稳定性验收（2026-10-12）

本轮在独立 MySQL、Redis 中启动实际 Web 和 Worker，使用 Chrome 操作网页。模型、搜索和 MCP 使用本地受控服务，模型服务严格检查工具调用与工具结果的对应关系。测试数据与开发数据库分离。

## 已复现并修复

| 问题 | 修复 | 回归测试 |
| --- | --- | --- |
| 只配置 `web.enabled: true` 时没有注册 Web 工具 | 启用默认工具，同时保留显式单项选择和工具过滤 | `TestWebEnabledUsesDefaultToolsAndPreservesExplicitSelection` |
| `wait_message` 只读第一千条消息，遗漏目标输入或最终回答 | 从目标输入开始，按游标读取后续页 | `TestCollaborationWaitBeyondFirstMessagePage` |
| 模型只返回角色、用量、空白或推理内容时，任务被错误标记成功 | 中间件处理后校验实际答案，空回答明确失败 | `TestModelStreamRejectsEmptyAnswer` |
| Chrome 调试连接接通后不响应，启动一直等待 | 启动限时 30 秒；超时清理，成功后解除启动期限 | `TestBrowser_UnresponsiveStartupTimesOut` |

四项回归均先在修复前复现失败，再在修复后通过。Chrome 超时后还验证了同一 profile 能重新启动真实 Chrome。

## 实际网页与进程验收

主流程共 22 项检查、故障流程共 6 项检查通过：

- 流式问答、多轮、长回答刷新、两个浏览器标签页使用不同任务。
- 文件创建、修改、读取、列目录和搜索；Shell 执行、非零退出和超时。
- 当前任务同一工具“始终允许”；其他工具和新任务仍需审批；拒绝后无文件副作用。
- 提问后刷新、Worker 重启后回答，继续原 Run。
- 计划、技能、Web 读取与搜索、本地 MCP、子代理、协作工具完整往返。
- 并行子代理分别允许或拒绝，核对实际生成的文件。
- 执行中追加输入只消费一次；取消模型或工具后，下一轮工具消息仍符合协议。
- 空回答和流连接中断明确失败，下一轮能继续。
- 历史压缩后继续对话和读取计划。

独立进程测试 `TestWebAndIndependentWorkerProcesses` 通过，覆盖审批重启、并行子代理审批、追问和工具执行期间 Worker 崩溃后的恢复。

## 自动检查

| 检查 | 结果 |
| --- | --- |
| `go build ./...` | 通过 |
| `go test -race -count=1 -timeout=2m ./...`，使用独立 MySQL/Redis | 通过 |
| `node --test deepagent/host/web/app.test.cjs` | 59/59 通过 |
| 修改文件的 gofmt、禁止 if 初始化语句检查、`git diff --check` | 通过 |
| 独立代码审查 | 未发现阻断问题 |

可使用专用测试存储复测：

```sh
export DEEPAGENT_TEST_MYSQL_DSN='专用测试数据库 DSN'
export DEEPAGENT_TEST_REDIS_ADDR='专用测试 Redis 地址'
go test -race -count=1 -timeout=2m ./...
node --test deepagent/host/web/app.test.cjs
```

## 验收边界

这轮没有完成外部 DeepSeek 实际模型、第三方 MCP、原生 Mac 桌面应用和真实 MLX 训练验收。外部模型测试被自动审批拒绝，需要用户明确允许专用合成测试数据发往 DeepSeek 后才能继续。

以上结果证明已覆盖场景通过，不能证明软件不存在任何 Bug。测试执行前的失败日志与修复后的日志分别保留，未将先前失败的整仓测试计作通过。
