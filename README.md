# DeepAgent

Go / Eino Graph 智能体。用户通过 Web 页面提交输入；独立 Worker 执行模型和工具。
Web 与 Worker 共用 MySQL / Redis，Manager 是进程内模块。

```text
Web → Manager → ThreadHost → DeepAgentThread → Run → DeepAgent → Eino Graph
                                                       ├─ Conversation
                                                       ├─ Registry / ToolExecutor
                                                       └─ Middleware / Checkpoint / Event
```

## 启动

需要仓库 go.mod 指定的 Go 版本、MySQL 8 和 Redis。

```bash
cp yaml/deepagent.example.yaml yaml/deepagent.yaml
export DEEPAGENT_MYSQL_DSN='deepagent:YOUR_PASSWORD@tcp(127.0.0.1:3306)/deepagent?parseTime=true'
export DEEPAGENT_MODEL='YOUR_MODEL_ID'
export DEEPAGENT_MODEL_BASE_URL='https://YOUR_PROVIDER/v1'
export DEEPAGENT_MODEL_API_KEY='YOUR_API_KEY'

# 终端 1：执行服务；可启动多个 Worker。
go run ./cmd/deepagent_worker --config yaml/deepagent.yaml

# 终端 2：唯一用户入口。
go run ./cmd/deepagent_web --config yaml/deepagent.yaml --root . --addr :8080
```

浏览器打开 http://localhost:8080。Web 和 Worker 必须使用相同的 Manager 配置。
模型凭据由 Worker 读取；本地配置文件已被 Git 忽略。没有 Worker 时，输入等待领取。
`scripts/install-sgadk.sh` 现在安装 Web 服务启动器，Worker 仍需单独启动。

## 代码主线

| 路径 | 职责 |
| --- | --- |
| `cmd/deepagent_web`, `deepagent/host/web` | 页面、HTTP 提交、审批恢复、历史查询 |
| `cmd/deepagent_worker`, `deepagent/worker` | Worker 启动和装配 |
| `deepagent/manager`, `deepagent/threadhost` | 调度、消息、lease、输出持久化 |
| `deepagent/core/runtime/agentthread` | Thread 生命周期、一次 Run、输入归属、事件 |
| `deepagent/core/graph` | 唯一 Agent Graph、模型、工具执行 |
| `deepagent/core/internal/conversation` | History / Compact / Usage |
| `deepagent/core/tools`, `deepagent/core/backend` | 工具定义及底层文件/命令能力 |
| `deepagent/core/middleware`, `deepagent/core/runtime/checkpointer` | 执行策略及 Eino checkpoint |

## 存储与恢复

Session 关联一组 Thread；Thread 是跨进程会话，Run 是一次可能包含多次模型和工具调用的执行。Worker 通过带有效期的 Permit 获得执行权；修改事件、历史、投递状态及执行状态需要当前 token。扫描到 Thread 不代表已取得执行权。

MySQL 保存调度、输入接收记录、事件、历史和记忆任务进度。Redis 提供消息投递缓存及实时通知，缓存丢失时从持久化输入记录重建。SQL 事务不会把 Redis 操作变成跨存储事务。持久化事件先写入数据库，再发送实时通知；文本增量和工具输出块不保证全部持久化，完整结果用于历史补读。

Worker 崩溃后，其他 Worker 可以在 Permit 过期后接管。已接收但没有完成依据的输入可重试。**外部工具副作用可能重复，系统不承诺恰好执行一次，也不自动撤销文件/网络操作。**

checkpoint 保存准确工具边界、消息、预算和恢复标识；恢复不会重新询问模型来猜测原来的工具调用。默认使用共享 MySQL，也可在 `checkpoint.backend` 选择 `redis` 或 `file`；文件模式必须设置共享 `checkpoint.path`。默认 MySQL checkpoint 写入与 Permit 校验在同一事务内；Redis/文件后端与调度库之间没有原子事务，严格隔离旧 Worker 写入的部署应使用默认后端。

跨机器部署时，所有 Worker 必须能访问 Thread 记录中的相同工作目录，以及配置的记忆/文件 checkpoint 路径。数据库共享不会同步文件。文件工具使用根目录约束；命令在宿主机工作目录执行，默认要求审批。这不等同于容器或操作系统安全沙箱。

自动上下文压缩默认以约 24000 tokens 为阈值（按历史字节数估算）；`compact_threshold_tokens` 为负数时关闭自动压缩。`keep_recent_messages` 默认保留最近六条并回退到完整用户轮次，摘要提交检查历史版本。审批默认针对本次工具调用，未启用跨调用自动复用的授权缓存。

## 可选能力

- **MCP**：`mcp` 配置 Streamable HTTP 服务，支持初始化、工具发现和调用。stdio 服务需显式 HTTP bridge。
- **Web**：`web.enabled: true` 注册页面读取；`web.search_url` 配置接受 `q` 参数或 `{query}` 占位符的搜索端点。
- **Skills**：从 `skill_paths` 发现技能目录并按需激活；未配置时检查工作目录的 `.agents/skills`、`.codex/skills`。
- **长期记忆**：`memory_enabled: true` 时设置 `memory_dir`。阶段一从来源历史提取，阶段二用内部 Agent 的文件工具整理 Markdown。处理凭证和产物基线持久化到 MySQL；启动时及 `memory_scan_interval` 周期扫描稳定历史。`memory_user_id` 可绑定稳定用户，默认按 Session 分组。
- **分布式子任务**：`create_task`、`send_task`、`wait_task`、`close_task` 使用 Manager 调度；等待父任务会占用执行名额，需要给子任务预留 Worker 并发。

## 验证

```bash
go test ./...
node --test deepagent/host/web/app.test.cjs
go test -race ./deepagent/core/... ./deepagent/manager/... ./deepagent/worker/... ./deepagent/host/...
go build ./cmd/deepagent_web ./cmd/deepagent_worker
```

真实 MySQL / Redis 与独立进程测试使用专用数据库：

```bash
export DEEPAGENT_TEST_MYSQL_DSN='root:TEST_PASSWORD@tcp(127.0.0.1:13316)/deepagent_test?parseTime=true'
export DEEPAGENT_TEST_REDIS_ADDR='127.0.0.1:16386'
bash scripts/test-distributed.sh
```

脚本构建临时 Web / Worker 二进制，以本机模拟模型验证 HTTP 提交、审批、
换 Worker 恢复和进程崩溃后的接管。未配置专用数据库时，进程测试明确跳过；
测试编译通过不代表已经完成真实服务验收。

Core 重构尚在进行，技术方案和迁移证据位于 `docs/superpowers/specs/`。
