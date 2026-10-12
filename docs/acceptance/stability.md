# 稳定性验收（2026-10-12）

本轮在独立 MySQL、Redis 中启动实际 Web 和 Worker，使用 Chrome 操作网页。主 Agent 的模型、搜索和 MCP 使用本地受控服务，模型服务严格检查工具调用与工具结果的对应关系；另用真实 MLX 模型和原生 Mac 应用补测本地能力。测试数据与开发数据库分离。

## 已复现并修复

| 问题 | 修复 | 回归测试 |
| --- | --- | --- |
| 只配置 `web.enabled: true` 时没有注册 Web 工具 | 启用默认工具，同时保留显式单项选择和工具过滤 | `TestWebEnabledUsesDefaultToolsAndPreservesExplicitSelection` |
| `wait_message` 只读第一千条消息，遗漏目标输入或最终回答 | 从目标输入开始，按游标读取后续页 | `TestCollaborationWaitBeyondFirstMessagePage` |
| 模型只返回角色、用量、空白或推理内容时，任务被错误标记成功 | 中间件处理后校验实际答案，空回答明确失败 | `TestModelStreamRejectsEmptyAnswer` |
| Chrome 调试连接接通后不响应，启动一直等待 | 启动限时 30 秒；超时清理，成功后解除启动期限 | `TestBrowser_UnresponsiveStartupTimesOut` |
| TextEdit 长文档的正文控件超出视口后被遗漏，操作只能退回容易过期的坐标点击 | 返回滚动视口内可见部分，保留完整控件边界校验；回退鼠标点击前用系统级命中测试拒绝遮挡 | `TestDesktopIntegrationVisibleTextArea` |

五项回归均先在修复前复现失败，再在修复后通过。Chrome 超时后还验证了同一 profile 能重新启动真实 Chrome。

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

## 本地能力补测

真实 Chrome 的另外 11 项检查通过：

- 实际 MLX 回答经 `Web → Worker → ask_local_model` 返回。
- 训练标记、带上前一轮、编辑训练副本、刷新恢复、取消标记、中英文切换；原对话不被修改。
- 原生 TextEdit 观察经 Web/Worker 返回；真实 Chrome 的打开、输入、按键、点击、滚动，以及旧观察和越界来源拒绝。
- Patch、glob、rg、语义搜索、Go 诊断和删除文件。
- 后台 Shell 跨同一 Thread 的下一次 Run 继续读取。
- 长期记忆提取、整理、持久化；Worker 重启后新任务仍能注入记忆。
- 无 JavaScript 异常或非预期 HTTP 错误。

原生 Mac 的 9 项检查通过：TextEdit 权限与观察、输入保存、旧观察拒绝、跨进程桌面占用与释放、长文档控件点击和滚动、6,624 个中文及 emoji 字符完整保存、超限输入无副作用、跨应用浮窗遮挡拒绝、移除遮挡后恢复点击。使用新建的专用文档，未输入到用户文档。长文档回归连续运行两次通过。

真实 MLX 自动训练链路通过：100 条专用确认样本满足调度条件后进行一次 QLoRA 迭代，生成约 2.9 MB 微调参数，状态为 `pending_validation`。训练占用设备时推理返回 `ErrModelBusy`，用户任务不会中断训练；训练后原模型仍可推理，显式加载候选参数也可推理。这验证训练流程，未验证训练质量，也未自动启用候选参数。测试只调整本次测试对象的空闲时间和迭代数，生产阈值不变。

MySQL 训练存储和训练消息 API 集成测试也通过，覆盖去重、删除来源、编辑副本、前一轮消息、大整数 ID、任务隔离及无效样本。

原生回归需要 macOS 系统权限和编译好的 helper：

```sh
DEEPAGENT_DESKTOP_TEST_HELPER='/编译好的/deepagent-computer' \
  go test -race -count=2 -v ./deepagent/graph/computer \
  -run '^TestDesktopIntegrationVisibleTextArea$'
```

## 自动检查

| 检查 | 结果 |
| --- | --- |
| `go build ./...` | 通过 |
| `go test -race -count=1 -timeout=2m ./...`，使用独立 MySQL/Redis | 通过 |
| `node --test deepagent/host/web/app.test.cjs` | 59/59 通过 |
| 修改文件的 gofmt、禁止 if 初始化语句检查、`git diff --check` | 通过 |
| 独立代码审查 | 未发现阻断问题 |

先停止可能领取测试任务或占用 MLX 设备的验收 Worker，再使用独立空库复测：

```sh
export DEEPAGENT_TEST_MYSQL_DSN='专用测试数据库 DSN'
export DEEPAGENT_LOCAL_MODEL_TEST_DSN="$DEEPAGENT_TEST_MYSQL_DSN"
export DEEPAGENT_TEST_REDIS_ADDR='专用测试 Redis 地址'
go test -race -count=1 -timeout=2m ./...
node --test deepagent/host/web/app.test.cjs
```

## 验收边界

这轮没有完成外部 DeepSeek 实际模型、第三方 MCP 和 Docker 工作区验收。外部模型测试被自动审批拒绝，需要用户明确允许专用合成测试数据发往 DeepSeek 后才能继续。

以上结果证明已覆盖场景通过，不能证明软件不存在任何 Bug。测试执行前的失败日志与修复后的日志分别保留，未将先前失败的整仓测试计作通过。
