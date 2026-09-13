# DeepAgent 技术架构与模块逻辑

本文描述当前工作区的实际实现，不是下一版改造方案。范围为业务代码，不包含测试代码；结论来自代码阅读，不代表本轮完成了运行验证。

## 1. 先用一句话理解系统

**CLI 提交消息，Manager 把消息变成可调度任务，Worker 领取执行权，Thread 管理会话内的 Run，Run 驱动 Eino 图调用模型和工具，执行事件再返回 CLI。**

这里最容易误解的是：Manager 是 Go 模块，不是单独部署的中央 RPC 服务。

```text
CLI 进程                                     Worker 进程（可以启动多个）
cmd/deepagent                                cmd/deepagent_worker
    │                                           │
终端界面 → host/runtime                     worker/managed
                │                               │
          Manager 实例                     Manager 实例
                │                               │
                └────── 共享 MySQL / Redis ──────┘
                                                │
                                      worker/distributed 装配
                                                │
                                        worker/thread 适配
                                                │
                                     core/agentthread.Thread
                                                │
                                               Run
                                                │
                                        core.DeepAgent
                                                │
                                           Eino Graph
                                          /          \
                                       模型           工具
```

CLI 与 Worker 没有直接 RPC 调用：它们通过同一套数据库和队列协作。Manager 的方法在调用方进程内执行，跨进程协调依靠共享存储和条件更新。

因此，可以在一台电脑上启动 MySQL、Redis、CLI 和多个 Worker，也可以把 Worker 放到不同机器。后一种部署还要解决工作目录、checkpoint 文件和长期记忆文件的共享问题，不能只共享数据库。

## 2. 必须区分的几个对象

| 对象 | 表达什么 | 生命周期和保存位置 |
| --- | --- | --- |
| Session | 一组相关会话线程，例如主线程和子任务线程 | 用 SessionID 关联线程与事件 |
| Manager Thread | 一个可领取、可暂停、可关闭的持久化会话 | MySQL，跨 Worker 存在 |
| Core Thread | 某个 Worker 当前加载的会话执行对象 | 进程内存，绑定上下文管理器和当前 Run |
| Run | Thread 内的一次执行，可包含多次模型和工具调用 | 内存运行对象；执行信息通过历史、事件和 checkpoint 保存 |
| Message | 用户输入、恢复输入或取消/关闭控制指令 | Redis 投递队列及 MySQL 消息归档 |
| Event | 执行过程中发生的事情，例如输出文本、工具完成、执行结束 | 持久化事件及实时事件通道 |
| Permit | 某个 Worker 在期限内执行 Thread 的凭证 | MySQL 中的 token、到期时间和持有者信息 |
| History | 下一次模型调用需要的历史消息与压缩记录 | 共享历史存储，加载到 Core 上下文管理器 |
| Checkpoint | 图被中断时的执行位置和状态 | 由配置选择文件或 Redis 等实现 |

Manager Thread 和 Core Thread **不是两个不同的用户会话**：前者负责跨进程调度，后者是领取后在进程中建立的执行实例。

Run 也不是一次模型请求：一个 Run 可以多次调用模型、执行多个工具，还可能吸收执行期间到达的额外输入。

## 3. 目录与职责

```text
deepagent/
├── cmd/
│   ├── deepagent/          CLI 启动入口
│   └── deepagent_worker/   独立 Worker 启动入口
├── backend/
│   ├── cli/tui/           终端交互和显示
│   ├── modelhub/          模型请求适配
│   └── sandbox/           保留的 Local/AIO 沙箱实现
├── host/runtime/          CLI 会话、提交、订阅和历史读取
├── manager/               调度状态、执行许可、消息和事件
│   ├── api/               面向调用方的 Go 接口门面
│   └── internal/          表结构、数据库、Redis、事件存储
├── worker/
│   ├── managed/           领取、续期、投递、输出和释放
│   ├── distributed/       模型、工具、历史等依赖装配
│   ├── thread/            Worker 协议与 Core 的适配
│   └── tasktool/          分布式子任务协作
├── core/
│   ├── agentthread/       会话上下文、Run 生命周期
│   ├── middleware/        提示词、工具和执行策略
│   ├── backends/          文件及命令执行后端
│   ├── checkpointer/      checkpoint 实现
│   ├── compact/           上下文压缩
│   └── memory/            长期记忆提取与整理
├── protocol/              输入、事件、timeline 协议
├── tools/                 保留的扩展工具
└── serialiser/            路径及数据转换纯函数
```

`backend` 不等于 HTTP 后端；`manager/frontend_api.go` 也不表示存在网页服务。当前用户入口只有 CLI。

## 4. CLI：收集输入和展示执行结果

代码入口：[main.go](/Users/bytedance/go/src/content/LLM/deepagent/cmd/deepagent/main.go)、[cli.go](/Users/bytedance/go/src/content/LLM/deepagent/cmd/deepagent/cli.go)。界面代码：[tui](/Users/bytedance/go/src/content/LLM/deepagent/backend/cli/tui)。

### 启动逻辑

1. 解析启动参数以及提示词输入来源。
2. 创建分布式 Runtime，连接共享 Manager 存储。
3. 有单次提示词时执行一次并输出结果；交互模式启动终端界面。
4. 界面把用户消息交给 Runtime，不自行构建模型或 Eino 图。

### 交互逻辑

终端界面使用消息驱动方式：输入、快捷键、模型输出、工具事件都进入更新逻辑，再由渲染逻辑生成画面。

- 普通输入：启动一次执行，消费返回的事件流。
- 模型文本：根据响应标识关联增量与完整消息，完整结果用于收束增量内容。
- 工具事件：按工具调用 ID 更新对应执行记录。
- 审批和追问：收集用户回答，提交恢复消息，不创建无关联的新问题。
- `/history`：查询共享事件构造的 Run 历史，不读取旧本地 Run JSON 文件。
- `/clear`：清空当前界面和当前线程引用，不删除数据库中的历史。
- 取消执行和退出界面是不同操作：退出/分离不应被理解为已经关闭服务端 Thread。

当前 TUI 在执行期间限制普通输入，所以 Core 支持的“当前 Run 接收追加消息”能力，不等于当前界面已完整开放。

## 5. Host Runtime：把 CLI 操作接到 Manager

核心代码：[thread_runtime.go](/Users/bytedance/go/src/content/LLM/deepagent/host/runtime/thread_runtime.go)、[subscription.go](/Users/bytedance/go/src/content/LLM/deepagent/host/runtime/subscription.go)、[interactive.go](/Users/bytedance/go/src/content/LLM/deepagent/host/runtime/interactive.go)。

### StartRun

1. 确保当前有一个 Manager Thread；没有则创建。
2. 确定读取事件的起点，并建立订阅。
3. 向 Manager 提交用户消息。
4. 返回 RunStream，由 CLI 消费事件、等待结束或发出取消。

先建立事件读取边界，再提交消息，是为了减少“Worker 执行很快，CLI 尚未开始监听”的遗漏窗口。

### 为什么同时读历史和实时流

MySQL 事件提供可补读记录，Redis 通道提供低延迟输出。订阅逻辑合并两路事件并去重，定期补拉持久化历史。

这不是两份独立的聊天记录，而是同一执行的“持久化读取”和“实时推送”。实时通道结束时还会补读历史，但不能把现有逻辑理解为无限自动重连。

### 如何识别当前执行

提交消息时，CLI 不一定已经知道 Worker 创建的 RunID。RunStream 根据事件中消费的消息 ID 关联当前请求，再绑定实际 RunID；同一输入因 Worker 崩溃而被重试时，也需要跟随新的执行。

注意一个现存命名问题：`ThreadRef.Namespace` 实际承载 SessionID，不能与 Manager 的 Namespace 混为一谈。

## 6. Manager：负责调度，不负责推理

主要代码：[frontend_api.go](/Users/bytedance/go/src/content/LLM/deepagent/manager/frontend_api.go)、[worker_api.go](/Users/bytedance/go/src/content/LLM/deepagent/manager/worker_api.go)、[manager.go](/Users/bytedance/go/src/content/LLM/deepagent/manager/manager.go)。

### 6.1 面向用户操作的方法

| 方法 | 实际逻辑 |
| --- | --- |
| CreateThread | 校验命名空间，创建 idle 线程；有首消息则入队并唤醒，再归档消息 |
| GetThread / ListSessionThreads | 按命名空间、线程或 Session 查询；列表使用数量/偏移控制，不需要调度扫描游标 |
| SubmitInput | 检查线程可接收输入，创建消息、入队、按状态唤醒，归档并返回 |
| ResumeFromBlock | 只恢复 blocked 线程；准备恢复消息或检查已有待处理消息；有任务则 ready，否则 idle |
| Cancel | 确定取消截止消息 ID，取消范围内未处理的普通消息，优先投递取消控制消息，再唤醒线程处理 |
| RequestThreadClose | 进入 closing，清理待处理普通消息，创建或复用关闭控制消息 |
| ConfirmThreadClosed | 条件更新到 closed，确认关闭控制消息，并处理重复确认 |
| ListEvents / ListSessionEvents / SubscribeSession | 历史事件查询及实时订阅 |

**CreateThread 的数据库写入与首消息入队不是一个原子操作。** 当前是先创建 idle 记录，再入队、唤醒；后续出错时，线程记录可能已经存在，不能把返回错误理解成没有任何副作用。

SubmitInput 唤醒失败会尝试移除刚入队的消息。Cancel 已经取消普通消息后，如果控制消息入队失败，不会自动把此前的取消全部还原。

### 6.2 面向 Worker 的方法

**ScanRunnableThreads：找候选。**

分别查询已经可以执行的 ready/closing 线程，以及 Permit 已过期的 running 线程；合并、排序、截取数量。扫描不授予执行权，多个 Worker 可以看到相同候选。

**ClaimThread：竞争执行权。**

生成新的 PermitToken 和期限，通过带状态/期限条件的更新竞争线程。取得许可后，在事务中加载线程和执行需要的消息；准备失败则用独立的限时上下文尝试释放，避免失败领取长期占住线程。

**RenewThreadPermit：保持执行权。**

更新条件包含线程 ID、当前 token、有效期限以及允许续期的状态。旧 Worker 不能仅凭 ThreadID 为自己续期。Worker 无法续期后，需要停止假定自己仍有执行权。

**ReadPendingInputs / ConfirmInputDelivery：读取和确认投递。**

读取前检查 Permit；Runtime 接收消息后确认投递。确认时对线程加行锁并检查执行权，然后更新队列和消息状态。

这里的“确认”只表示 **执行层接收了消息**，不表示模型完成，更不表示工具操作成功。

**ReleaseThread：把线程交还调度。**

有待处理消息则 ready，没有则 idle，需要用户干预则 blocked。释放到 idle 后再检查一次队列，是为了避免新消息恰好在释放过程中到达而无人唤醒。

### 6.3 Thread 状态变化

| 当前情况 | 动作 | 结果 |
| --- | --- | --- |
| 新建且没有首消息 | CreateThread | idle |
| idle 收到需要执行的消息 | 入队并唤醒 | ready |
| ready 被 Worker 成功领取 | ClaimThread | running，设置 Permit |
| running 正常交还，队列为空 | ReleaseThread | idle |
| running 正常交还，仍有输入 | ReleaseThread | ready |
| 执行停在审批/追问等等待点 | 带阻塞信息释放 | blocked |
| blocked 被恢复 | ResumeFromBlock | 有消息为 ready，无消息为 idle |
| running 的 Permit 过期 | 新 Worker 竞争接管 | running，替换 Permit |
| 请求关闭 | RequestThreadClose | closing |
| Worker 完成关闭处理 | ConfirmThreadClosed | closed |

取消一次 Run 不等于关闭整个 Thread；blocked 也不是执行故障，它可以表示正常等待用户回答。

### 6.4 数据库与 Redis 的分工

实现：[db.go](/Users/bytedance/go/src/content/LLM/deepagent/manager/internal/storage/db/db.go)、[input_queue.go](/Users/bytedance/go/src/content/LLM/deepagent/manager/internal/storage/input_queue.go)。

- 表结构描述数据，Filter 组织查询条件，数据库方法负责 GORM 操作。
- 事务通过上下文传递；`ForUpdate` 是事务内的悲观行锁，不是普通查询自动加锁。
- `UpdateAndGet` 是更新后读主库，仍然是两步数据库操作，不是 MySQL 自动返回整行。
- Redis 保存待投递消息及队列顺序，MySQL 保存调度状态和消息归档。
- 消息归档部分路径是尽力写入：归档失败与投递失败不是同一种结果。

MySQL 事务不能把其中调用的 Redis 操作一起回滚。当前可靠性依靠条件更新、清理、复查和恢复补偿，而不是跨存储事务。

## 7. Worker Managed：执行调度循环

入口：[worker.go](/Users/bytedance/go/src/content/LLM/deepagent/worker/managed/worker.go)。消息和输出逻辑：[input_processor.go](/Users/bytedance/go/src/content/LLM/deepagent/worker/managed/input_processor.go)、[output_processor.go](/Users/bytedance/go/src/content/LLM/deepagent/worker/managed/output_processor.go)。

### 一个 Worker 如何工作

1. 定期扫描候选线程。
2. 获取本进程并发名额，限制同时执行的线程数。
3. 竞争线程 Permit；竞争失败不执行该线程。
4. 启动续期，构建并初始化对应 Runtime。
5. 持续把队列输入送入 Runtime，同时接收 Runtime 输出。
6. 正常空闲、阻塞、关闭、执行权丢失或进程退出时，结束这次持有。
7. 清理 Runtime，按原因释放线程或确认关闭。

### 输入不是全部同样处理

- 普通消息交给 Runtime，接收成功后确认投递。
- 恢复消息携带已有 Run/checkpoint 的恢复信息。
- 取消控制消息要求中断相应执行，不能当作普通提示词塞给模型。
- 关闭控制消息进入关闭流程，处理后确认 Thread 关闭。

因此输入处理器存在控制分支，是业务语义不同，不只是消息格式不同。

### 输出与交还的顺序

执行事件经输出处理器发布到 Manager；运行时同时可以给出需要阻塞或交还的信号。交还前需要处理已有输出，避免状态已变 idle/blocked，但完成或审批事件还留在进程缓冲中。

当前发布有有限重试；重试耗尽存在记录日志后放弃该事件的路径，因此不能宣称事件绝不丢失。

### 停机与崩溃不同

正常停机先停止接收新工作，并给正在执行的任务时间结束；必要时再中断，等待收尾。等待期间仍需维持 Permit。

硬崩溃无法主动收尾，只能由其他 Worker 在 Permit 过期后接管。已经执行过的外部工具动作不会因接管而自动撤销。

## 8. Worker Distributed：装配一次执行所需的依赖

启动：[bootstrap/run.go](/Users/bytedance/go/src/content/LLM/deepagent/worker/distributed/bootstrap/run.go)。装配：[agent_builder.go](/Users/bytedance/go/src/content/LLM/deepagent/worker/distributed/agent_builder.go)。

### 启动时装配

读取 YAML → 初始化日志 → MySQL/Redis → checkpoint 工厂 → 模型集合 → MCP 工具 → Manager → 历史存储、线程引用、审批记录 → 可选长期记忆 → 启动 Managed Worker。

模型密钥与模型选择属于 Worker 配置；CLI 不负责模型调用。

### 领取线程后装配

1. 从持久化 Thread 的角色、目录等信息确定执行配置。
2. 选择模型、预算和允许的能力。
3. 准备工作目录、事件通道、历史和 checkpoint。
4. 设置执行策略、提示词、Skills、协作工具和上下文压缩。
5. 创建 Core Thread，再包装成 Managed Worker 能使用的 Runtime。

当前普通线程创建的是 `SandboxFilesystemBackend`，根目录设为工作目录。**这不是自动创建 AIO 容器。**

### 计划和审批策略

计划模式约束可用能力，执行策略判断命令是否需要审批；审批中断后由用户回答恢复。审批记忆目前是进程内存记录，不是跨 Worker 自动共享的持久化授权。

## 9. Worker Thread：协议适配，不是另一套 Agent Loop

核心：[thread_runtime.go](/Users/bytedance/go/src/content/LLM/deepagent/worker/thread/thread_runtime.go)、[event_mapper.go](/Users/bytedance/go/src/content/LLM/deepagent/worker/thread/event_mapper.go)、[output_bridge.go](/Users/bytedance/go/src/content/LLM/deepagent/worker/thread/output_bridge.go)。

### 输入方向

`PostMessage` 解码并区分普通输入、恢复输入和压缩指令。

- 普通输入转换为 Core 消息，调用 `DeepAgentThread.SubmitInput`。
- 恢复输入校验 RunID、checkpoint、中断及回答，调用 `ResumeRun`。
- 压缩指令进入上下文压缩流程，不当作普通用户对话执行。

### 输出方向

Core 事件 → 补充线程、Run、消息关联 → 转换为通用事件 → 输出桥接队列 → Managed Worker。

模型文本增量、工具输出块等用于实时显示；完整结果、执行结束、中断等事件用于记录执行事实。并非每一条流式增量都持久化。

该层还负责把“需要审批/追问”的 Core 中断映射成 Worker 能理解的阻塞信息。Manager 无需理解 Eino 的中断对象内部结构。

## 10. Core AgentThread：管理上下文和当前 Run

主要代码：[thread.go](/Users/bytedance/go/src/content/LLM/deepagent/core/agentthread/thread.go)、[run.go](/Users/bytedance/go/src/content/LLM/deepagent/core/agentthread/run.go)、[context_mng.go](/Users/bytedance/go/src/content/LLM/deepagent/core/agentthread/context_mng.go)。

### SubmitInput 的两个分支

1. 当前没有 Run：生成 RunID，构建本次 Agent，记录为当前 Run，异步开始执行。
2. 当前有 Run：尝试把输入加入当前 Run 的 pending 队列，返回该 Run 的句柄，而不是并行再建一个 Run。

当前 Run 不再接收输入时，追加可能返回错误，不能把“有 current”理解成一定能够插入。

### pending 输入何时交给模型

输入不会任意插入正在进行的模型请求。上下文中间件在模型调用边界读取 pending，将它们加入接下来的模型上下文。

模型想结束时，Thread 检查是否还有 pending：有则让图继续，没有则关闭本轮接收并结束。这样避免模型刚说完就遗漏同时到达的输入。

### Run 结束为何还要等 eventsDrained

`executeRun` 的顺序是：执行结束 → 关闭本 Run 事件源 → 等待事件转发完成 → 清理当前 Run。

`<-current.eventsDrained` 等待的是 **事件已经转发到线程事件通道**，不是等模型，也不是保证事件已经写入数据库。

它避免先把 current 清掉、新 Run 已经开始，而旧 Run 的末尾事件仍未完成内部转发。

### Run 的职责

Run 调用 `DeepAgent.Stream`，消费模型流，等待相关回调收尾，并发出结束事件。错误、中断和正常完成走不同分支；中断需要附带 checkpoint、原因和用户可恢复的信息。

这层关心“本次执行是否完成、消费了哪些输入、怎么恢复”，不负责竞争分布式 Permit。

## 11. Core DeepAgent：构建并运行 Eino 图

构造：[constructor.go](/Users/bytedance/go/src/content/LLM/deepagent/core/constructor.go)。执行：[agent.go](/Users/bytedance/go/src/content/LLM/deepagent/core/agent.go)。图：[graph_builder.go](/Users/bytedance/go/src/content/LLM/deepagent/core/graph_builder.go)。

### 构建过程

配置和默认值 → 后端与中间件链 → 图状态与 checkpoint → 收集/过滤工具 → 工具绑定模型 → 添加节点与连线 → 编译 Runnable。

Run/Stream 在调用 Runnable 前准备上下文、回调、checkpoint 选项和恢复信息。模型执行不由 Manager 或 CLI 直接控制。

### 图的实际路由

```text
START → model
          ├── 有工具节点，且模型返回工具调用 → tools ──→ model
          ├── 无工具调用，且 Thread 要求继续 → continue ──→ model
          └── 不需要继续 → END
```

`tools` 执行模型选择的工具，并把结果送回下一次模型调用。

`continue` 本身不执行用户命令，也不调用模型；它消费完输入流并返回空消息集合，使图能够再次进入 model。真正追加 pending 输入发生在下一次模型调用的上下文准备阶段。

当前路由直接使用节点名，没有再使用 `ToExecutor → Continue` 两套枚举映射。

### 两个预算

- `MaxSteps` 限制图执行步数。
- `MaxModelCalls` 限制模型调用次数。

两者计数对象不同：工具和继续节点也会参与图执行，不能直接把图步数当成模型次数。

## 12. 中间件、工具与沙箱

代码：[middleware](/Users/bytedance/go/src/content/LLM/deepagent/core/middleware)、[core/tools](/Users/bytedance/go/src/content/LLM/deepagent/core/tools)、[backends](/Users/bytedance/go/src/content/LLM/deepagent/core/backends)。

中间件通过构建上下文、模型调用前后和工具包装等扩展点工作，不是每个中间件都运行一套 Loop。

| 模块 | 代码逻辑 |
| --- | --- |
| baseprompt | 组织基础指令，进入模型上下文 |
| contextmanager | 读取历史、追加输入和模型/工具结果，与 Thread 上下文管理器衔接 |
| patchtoolcalls | 修补上下文中不完整的工具调用/结果配对 |
| repairjson | 修复工具参数的 JSON 格式问题 |
| filesystem | 提供文件读取、写入和检索能力，调用后端执行 |
| execute | 注册命令执行能力，结合策略判断和审批 gate 执行 |
| plan | 管理计划更新，转换成计划事件 |
| planmode | 限制计划阶段行为，并支持需要用户回答的交互 |
| skill | 发现和加载技能内容，激活后加入执行上下文 |
| web | Web 搜索和页面读取工具 |
| subagent | Core 内部子 Agent 调用，不等于分布式子任务调度 |

### 两套后端能力要分清

当前 Worker 主链使用 Core 文件系统后端；仓库还保留 `backend/sandbox` 的 Local/AIO 实现，以及 `tools` 的扩展工具。

保留实现不等于主链已注册。当前扩展工具和 AIO 沙箱尚未接入普通 Worker 的默认装配路径，不能按“代码存在”判断用户已经在使用容器执行。

文件系统根目录和命令分类也不能等同于完整安全沙箱：路径检查、软链接和宿主机命令执行都有各自边界。

## 13. 历史、压缩和 checkpoint

代码：[rollout_gorm_store.go](/Users/bytedance/go/src/content/LLM/deepagent/core/agentthread/rollout_gorm_store.go)、[compact](/Users/bytedance/go/src/content/LLM/deepagent/core/compact)、[checkpoint 工厂](/Users/bytedance/go/src/content/LLM/deepagent/worker/distributed/bootstrap/internal/checkpoint/store.go)。

### 历史

ContextManager 初始化时加载有效历史；用户输入、模型回答、工具结果按顺序追加。共享序号帮助多次加载维持顺序。

它保存的是模型上下文，不是终端界面的所有显示事件。工具输出如何进入模型，与如何展示在终端，是不同路径。

### 压缩

达到上下文阈值或收到压缩指令后，保留近期上下文，并用模型总结较早内容。还需要处理工具调用与结果配对，避免压缩后上下文结构不合法。

压缩期间历史可能变化，因此代码检查版本，避免旧上下文生成的摘要覆盖新输入。压缩记录也会持久化，重载时从有效压缩边界恢复。

### checkpoint

checkpoint 记录中断点及图状态。审批恢复需要它，只有聊天历史无法准确恢复“某个工具执行前等待批准”的位置。

文件 checkpoint 适合同机且目录共享的 Worker；跨机器接管需要使用共同可访问的 checkpoint 存储。Redis/MySQL 共享并不会自动同步本地文件。

## 14. 分布式子任务协作

代码：[tasktool](/Users/bytedance/go/src/content/LLM/deepagent/worker/tasktool)。

1. 主 Agent 调用协作工具创建子任务。
2. 工具通过 Manager 创建同 Session 下的子 Thread，并提交任务输入。
3. 任意可用 Worker 领取子 Thread，运行同样的 Core 执行链。
4. 主任务可发送补充消息、等待结果或请求关闭子线程。
5. 等待逻辑读取子线程的事件和状态，判断完成、失败、阻塞或超时。

线程引用存储把可读名称关联到 ThreadID。关闭请求被接受，不表示子线程已经完成关闭；等待超时也不等于子任务被自动取消。

这条链路使用真正的分布式调度；Core 的 subagent 中间件是在另一层提供子 Agent 能力，不能把二者视为同一实现。

## 15. 长期记忆

代码：[core/memory](/Users/bytedance/go/src/content/LLM/deepagent/core/memory)、[worker/distributed/memory.go](/Users/bytedance/go/src/content/LLM/deepagent/worker/distributed/memory.go)。此功能受配置开关控制。

长期记忆不是把全部历史再次传给模型，而是从执行历史提取以后有用的内容。

### 第一阶段：从单个来源提取

扫描满足条件的来源 → 竞争处理权 → 读取历史 → 调模型提取记忆 → 保存原始记忆和摘要。

### 第二阶段：按用户整理

收集阶段一产物 → 准备记忆工作目录 → 创建内部整理任务 → Agent 使用工具更新记忆文件 → 检查产物 → 保存处理基线和进度。

后续普通 Agent 通过记忆读取中间件获得摘要，不必加载全部原始记录。

该模块有自己的后台任务和处理期限；它们与普通 Thread 的 Permit 解决的是不同任务的并发问题，不是重复控制同一个 Run。

## 16. 事件存储和协议

代码：[protocol](/Users/bytedance/go/src/content/LLM/deepagent/protocol)、[eventlog](/Users/bytedance/go/src/content/LLM/deepagent/manager/internal/service/eventlog/service.go)、[streamout](/Users/bytedance/go/src/content/LLM/deepagent/manager/internal/service/streamout/service.go)。

### 输入协议

区分普通消息、恢复消息和控制指令；内容可以包含文本或多媒体。恢复消息需要明确对应的 Run/checkpoint/中断，而不是只有一个“继续”字符串。

### 输出协议

Core 事件表达执行内部事实，Worker 协议表达执行层输出，timeline 表达消费方可读取的时间线。转换层负责关联 ID、整理载荷和控制可见字段，不只是机械改字段名。

### 存储分工

| 数据 | 主要存储 | 作用 |
| --- | --- | --- |
| Thread、状态、Permit | MySQL | 调度和竞争执行权 |
| 待处理/已接收输入及载荷 | Redis | 投递和接管恢复 |
| 消息归档 | MySQL | 留存消息事实 |
| 持久化事件 | MySQL | 历史查询、补读、恢复判断 |
| 实时事件及订阅位置 | Redis | 低延迟显示 |
| 模型历史与压缩记录 | MySQL 历史表 | 重建上下文 |
| checkpoint | 配置选择的存储 | 恢复图执行 |
| 长期记忆任务/产物记录 | MySQL，另有工作区文件 | 提取进度和记忆内容 |

持久化事件通常先落库，再进入实时通道；纯实时事件不一定落库。历史补读能补回已持久化的事件，不能补回所有丢失的 token 增量。

## 17. 四条完整业务链

### 普通对话

CLI 输入 → Runtime 确保 Thread 并订阅 → Manager 入队并置 ready → Worker 领取 Permit → 构建 Runtime → PostMessage → Core 创建 Run → Eino 模型/工具循环 → 事件回传 Manager → CLI 显示 → Worker 空闲后交还 Thread。

### 审批或追问恢复

工具/策略发起中断 → 保存 checkpoint 并输出中断事件 → Worker 把 Thread 释放为 blocked → CLI 展示问题 → 用户回答 → Manager 恢复并优先入队 → Worker 再次领取 → Runtime 根据原 Run/checkpoint 恢复 → 图继续执行。

### 取消当前执行

CLI 发取消 → Manager 确定截止范围并取消待处理普通输入 → 优先入队控制消息 → Worker 中断对应执行 → 发布中断结果 → 根据剩余工作交还 Thread。此过程不等同于删除历史或关闭整个会话。

### Worker 硬崩溃

续期停止 → Permit 过期 → 其他 Worker 扫描并接管 → 检查已接收消息及其结束/中断事件依据 → 对缺少完成依据的普通输入重新入队 → 加载共享历史和所需状态 → 再次执行。

**这是允许重试的恢复机制，不是工具副作用恰好执行一次的保证。** 崩溃前已写入文件、发送请求或调用外部接口，重试可能再次执行；当前采用的是用户已接受的这一语义。

## 18. 当前架构的边界与注意事项

1. 唯一用户入口是 CLI，没有恢复网页、HTTP 产品接口或服务发现。
2. 唯一产品执行路径经 Manager 和独立 Worker，不存在 CLI 直接调用 Core 的第二条运行链。
3. MySQL 与 Redis 的状态变化并非一个事务，错误返回可能发生在部分副作用之后。
4. 共享数据库不等于共享工作目录；多机部署需要另外保证文件和 checkpoint 可达。
5. AIO 和扩展工具已保留，但未接入当前默认 Worker 链路。
6. Permit 防止正常情况下多个 Worker 同时合法持有执行权，不能撤销已发出的外部工具动作。
7. 代码中仍有 cloud、turn、lease 等历史标签；部分用于序列化兼容，部分是未清理命名，不代表又存在一套产品链路。
8. 实时输出、消息归档、事件发布包含尽力处理或有限重试，不能一概称为强一致、无损。

## 19. 推荐读代码的顺序

不要先从全部结构体字段开始。沿一条用户消息追踪更容易理解：

1. [CLI 启动](/Users/bytedance/go/src/content/LLM/deepagent/cmd/deepagent/cli.go)。
2. [ThreadRuntime.StartRun](/Users/bytedance/go/src/content/LLM/deepagent/host/runtime/thread_runtime.go:42)：提交前做什么。
3. [Manager 用户接口](/Users/bytedance/go/src/content/LLM/deepagent/manager/frontend_api.go)：输入怎么进入队列。
4. [Worker.Run](/Users/bytedance/go/src/content/LLM/deepagent/worker/managed/worker.go:239)：谁领取并执行。
5. [newAgentThread](/Users/bytedance/go/src/content/LLM/deepagent/worker/distributed/agent_builder.go:191)：模型和工具从哪里来。
6. [Runtime.PostMessage](/Users/bytedance/go/src/content/LLM/deepagent/worker/thread/thread_runtime.go:186)：消息怎样变成 Core 调用。
7. [DeepAgentThread.SubmitInput](/Users/bytedance/go/src/content/LLM/deepagent/core/agentthread/thread.go:139)：怎样创建或复用 Run。
8. [run.execute](/Users/bytedance/go/src/content/LLM/deepagent/core/agentthread/run.go:90)：怎样启动图。
9. [buildGraphWithConfig](/Users/bytedance/go/src/content/LLM/deepagent/core/graph_builder.go:47)：模型、工具和继续节点怎样连接。
10. 最后反向阅读事件桥接、Manager 发布与 Host 订阅，理解结果如何回来。

先理解这条主线，再读审批、取消、接管、长期记忆，复杂度会小很多。
