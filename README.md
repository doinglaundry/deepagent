# DeepAgent / SGADK CLI

基于 Go、Eino Graph 和 Bubble Tea 的终端智能体。CLI 提交输入、订阅事件；独立 Worker 调用模型和工具。多个进程通过同一套 MySQL 和 Redis 协作，Manager 是进程内 Go 模块，没有独立的中央 RPC 服务。

```text
cmd/deepagent → host/runtime → Manager ← worker/managed ← cmd/deepagent_worker
                                  │             │
                              MySQL/Redis   worker/thread
                                                │
                                         core/agentthread
                                                │
                                    Eino model → tools → model
                                           ↘ continue ↗
```

## 启动

需要 Go 1.25+、MySQL 8 和 Redis。CLI 和 Worker 必须使用相同的 Manager 配置。Worker 还需要模型配置。

```bash
cp yaml/deepagent.example.yaml yaml/deepagent.yaml
```

编辑本地 YAML，并通过环境变量提供连接信息和模型凭据：

```bash
export DEEPAGENT_MYSQL_DSN='deepagent:YOUR_PASSWORD@tcp(127.0.0.1:3306)/deepagent?parseTime=true'
export DEEPAGENT_MODEL='YOUR_MODEL_ID'
export DEEPAGENT_MODEL_BASE_URL='https://YOUR_PROVIDER/v1'
export DEEPAGENT_MODEL_API_KEY='YOUR_API_KEY'
```

`yaml/deepagent.yaml` 已被 Git 忽略。模型支持 OpenAI 兼容接口、Claude/Anthropic 和 Kimi/Moonshot；`model` 保留配置中的原始模型 ID。CLI 只解析 `manager` 配置，不需要读取模型密钥。

可选：使用仓库的 Compose 文件启动本地数据库。先设置 `DEEPAGENT_DB_PASSWORD` 和 `DEEPAGENT_DB_ROOT_PASSWORD`，并让 DSN 中的密码与前者一致：

```bash
docker compose up -d
```

分别在两个终端启动：

```bash
# 终端 1：可在其他终端再启动相同命令，增加 Worker 数量。
go run ./cmd/deepagent_worker --config yaml/deepagent.yaml

# 终端 2：交互 CLI。
go run ./cmd/deepagent --config yaml/deepagent.yaml
```

`go run .` 保留为相同分布式 CLI 的兼容入口。没有 Worker 时，输入会等待领取，不会退回本地模型执行。

单次执行和重新连接：

```bash
go run ./cmd/deepagent --prompt '理解这个项目'
go run ./cmd/deepagent --json --prompt '检查文件'    # 协议事件 JSONL
cat task.txt | go run ./cmd/deepagent --prompt -
go run ./cmd/deepagent --thread THREAD_ID --prompt '继续处理'
```

`--root` 优先于 `SGADK_ROOT`，其次使用当前目录。`--session` 将相关 Thread 关联起来；`--thread` 附着已存在的 Thread，并验证 Session 是否匹配。单次 CLI 会在标准错误输出中显示 SessionID 和 ThreadID。遇到审批/追问时保留服务端阻塞状态，重新使用 `--thread` 提交回答；工具审批可回答 `yes` 或 `no`。

全局命令安装仍可运行 `bash scripts/install-sgadk.sh`，它构建 CLI wrapper；Worker 需要单独启动。

## 交互

| 操作 | 行为 |
| --- | --- |
| 普通输入 | 提交到共享 Thread，显示模型增量和工具调用结果 |
| 工具审批 | `y` 同意、`n` 拒绝；回答恢复原 Run/checkpoint |
| 追问 | 显示问题，下次输入作为对应中断的回答 |
| `/history` | 从持久化事件读取运行历史；Enter 查看，不恢复旧本地文件 |
| `/clear` | 清空当前界面并分离当前 Thread；保留服务端历史 |
| `/plan on/off` | 修改后续执行的能力限制，计划阶段过滤写入/执行等工具 |
| `/compact` | 压缩当前共享模型上下文，保留近期消息和工具配对 |
| `/todos` | 切换计划面板 |
| `/close` | 请求关闭服务端 Thread；请求接受不代表 Worker 已完成关闭 |
| `/exit`、`/quit` | 退出界面，不关闭服务端 Thread |
| Esc / 执行中 Ctrl-C | 请求取消当前执行，区别于退出与关闭 |
| Ctrl-O | 展开/折叠最近一个长工具输出 |

Core 支持在运行中的模型边界吸收追加输入；当前 TUI 执行期间限制普通输入。

## 模块边界

| 目录 | 职责 |
| --- | --- |
| `cmd/deepagent`, `host/cli` | CLI 参数、配置和启动 |
| `backend/cli/tui` | 终端交互、渲染与协议事件映射 |
| `host/runtime` | 订阅前建立事件边界、提交、增量/历史合并、恢复和取消 |
| `manager`, `manager/api` | Thread 状态机、Permit、消息接收、事件、共享历史和存储门面 |
| `worker/managed` | 扫描/领取、续期、投递确认、输出排空、释放及关闭 |
| `worker/distributed`, `backend/modelhub` | Worker 配置、模型、MCP、Skills、Web、记忆和存储装配 |
| `worker/thread` | Worker 输入/输出协议与 Core Thread 的适配 |
| `worker/tasktool` | 同 Session 下真实分布式子任务：创建、发送、等待、关闭 |
| `core`, `core/agentthread` | Eino 图、模型/工具预算、当前 Run、pending、checkpoint 恢复 |
| `core/backends` | 根目录文件访问、检索、编辑、命令、追问和计划工具 |
| `core/checkpointer`, `core/compact`, `core/memory` | checkpoint 后端、上下文压缩和两阶段记忆 |
| `protocol` | 输入、多媒体、恢复、事件和关联 ID |
| `backend/sandbox` | 保留的 Local/AIO 实现，普通 Worker 不会自动创建 AIO 容器 |

原 `backend/agent`、本地 Run/checkpoint/rollback 实现及其测试保留用于兼容和扩展参考。当前 CLI 启动链使用共享 Runtime，不调用旧 DeepAgent Runtime，也不把 `.eino-cli/sessions` 的 Run JSON 当成共享历史。

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
go test -race ./manager/... ./core/... ./worker/... ./host/... ./backend/cli/tui
go build ./cmd/deepagent ./cmd/deepagent_worker
```

真实 MySQL/Redis 与独立进程集成测试使用专用测试数据库：

```bash
export DEEPAGENT_TEST_MYSQL_DSN='root:TEST_PASSWORD@tcp(127.0.0.1:13316)/deepagent_test?parseTime=true'
export DEEPAGENT_TEST_REDIS_ADDR='127.0.0.1:16386'
bash scripts/test-distributed.sh
```

脚本构建临时 CLI/Worker 二进制。测试使用本机模拟模型服务，不消耗真实模型额度；覆盖竞争执行、共享历史、审批阻塞及换 Worker 后恢复，以及模型请求期间强杀 Worker 后的租约接管。未设置数据库环境变量时，外部服务测试会显式跳过。

用户提供的设计原文见 [技术架构](docs/superpowers/specs/2026-09-12-deepagent-architecture.md)，实施记录见 [重构计划](docs/superpowers/plans/2026-09-12-distributed-deepagent.md)。
