# DeepAgent 本地日志系统技术方案

状态：设计稿。以下 Go、Alloy、Compose 配置是拟实现代码，尚未接入或启动。

## 1. 数据流与文件

```text
Web    → web.jsonl    ┐
Worker → worker.jsonl ├→ Alloy → Loki → Grafana（127.0.0.1:3000）
MLX    → 原有进程日志 ┘

Agent 任务菜单 → Grafana Explore（自动带 thread_id / run_id 查询）
```

```text
deepagent/logging/
  logging.go                       # 初始化 + context 中的 logger

deploy/logging/
  compose.yaml                     # 独立于业务服务启动
  alloy.alloy
  loki.yaml
  grafana/
    provisioning/datasources/loki.yaml
    provisioning/dashboards/default.yaml
    dashboards/deepagent-logs.json

.eino-cli/logs/
  web.jsonl
  worker.jsonl
  web.stderr.log                   # 启动器捕获 panic、启动失败及原始 stderr
  worker.stderr.log
```

新增 Go 依赖仅 `gopkg.in/natefinch/lumberjack.v2`：滚动文件。不新增日志数据库表、消息队列、Logger 接口或日志 Middleware。

## 2. 核心 Go 代码

拟新增 `deepagent/logging/logging.go`：

```go
package logging

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"

	"gopkg.in/natefinch/lumberjack.v2"
)

// ConfigureLogger 每个进程启动时调用一次；一个文件只有一个进程写入。
func ConfigureLogger(serviceName, logDirectory string, logLevel slog.Level) (*lumberjack.Logger, error) {
	err := os.MkdirAll(logDirectory, 0700)
	if err != nil {
		return nil, err
	}

	logFile := &lumberjack.Logger{
		Filename:   filepath.Join(logDirectory, serviceName+".jsonl"),
		MaxSize:    50, // 每个文件最多 50 MB。
		MaxBackups: 5,
		MaxAge:     7, // 最多保留七天；数量限制可能更早清理。
		Compress:   false,
	}

	handler := slog.NewJSONHandler(logOutput{logFile: logFile}, &slog.HandlerOptions{
		Level:     logLevel,
		AddSource: true,
	})
	logger := slog.New(handler).With("service", serviceName, "pid", os.Getpid())
	slog.SetDefault(logger)
	return logFile, nil
}

// 只负责写入兜底：文件失败改写 stderr，stderr 采集失败不影响文件写入。
type logOutput struct{ logFile *lumberjack.Logger }

func (output logOutput) Write(data []byte) (int, error) {
	written, err := output.logFile.Write(data)
	if err != nil {
		return os.Stderr.Write(data)
	}
	return written, nil
}

// context 只保存会话、Run 等稳定字段；component、operation 每条日志自己写。
type loggerContextKey struct{}

func WithLogFields(ctx context.Context, fields ...any) context.Context {
	logger := GetLogger(ctx).With(fields...)
	return context.WithValue(ctx, loggerContextKey{}, logger)
}

func GetLogger(ctx context.Context) *slog.Logger {
	logger, exists := ctx.Value(loggerContextKey{}).(*slog.Logger)
	if exists {
		return logger
	}
	return slog.Default()
}
```

入口在配置、数据库和模型初始化之前调用：

```go
logFile, err := logging.ConfigureLogger("worker", ".eino-cli/logs", slog.LevelInfo)
if err != nil {
    slog.Error("initialize file logging failed; using stderr", "error", err)
}
if logFile != nil {
    defer logFile.Close()
}

// Web 入口相同，把 "worker" 换成 "web"。
```

日志目录初始化失败保留默认 stderr logger。现有 `config.SetLogLevel` 写入 `io.Discard`，两个主入口改用这里的初始化，不接入该旧函数。

## 3. Thread → Run → Graph 字段传递

`Worker.RunThread` 校验 claim 后，沿用现有 context：

```go
ctx = logging.WithLogFields(ctx,
    "thread_id", strconv.FormatInt(claim.Thread.ThreadID, 10),
    "session_id", claim.Thread.SessionID,
)

// 原有 startLease、createThread、输入投递沿用该 ctx。
```

`Run.Execute` 入口：

```go
ctx = logging.WithLogFields(ctx, "run_id", r.id)
logging.GetLogger(ctx).InfoContext(ctx, "run started",
    "component", "run",
    "operation", "run.execute",
)
```

跨进程通过已有 `message_id`、`thread_id` 关联，不假设 Go context 能跨 MySQL 传递。恢复沿用原 Run ID；pid、时间和 resume 事件区分本次执行。子图保留顶层任务与 Run 字段，额外记录 `graph_run_id`、`agent_name`、`agent_depth`、`parent_tool_call_id`，不把子图 ID 覆盖成父 Run ID。

## 4. 直接修复两处日志缺口

`Worker.Run` 领取失败时补一条日志，原来的重试逻辑保持不变：

```go
claim, acquireErr := w.Client.Acquire(ctx, agentmodel.AcquireRequest{LeaseMS: w.LeaseMS})
if acquireErr != nil {
    logging.GetLogger(ctx).ErrorContext(ctx, "acquire thread failed",
        "component", "manager",
        "operation", "thread.acquire",
        "error", acquireErr,
    )
}
// 后面继续现有的等待、归还槽位、重试逻辑；没有待领取任务不写错误日志。
```

原来 `_ = w.RunThread(...)` 改为读取错误并记录：

```go
runErr := w.RunThread(context.WithoutCancel(ctx), ctx, &claim)
if runErr != nil {
    logging.GetLogger(ctx).ErrorContext(ctx, "thread execution ended with error",
        "component", "worker",
        "operation", "thread.execute",
        "thread_id", strconv.FormatInt(claim.Thread.ThreadID, 10),
        "error", runErr,
    )
}
```

以上片段展示错误记录位置。接入时按现有生命周期结果区分正常取消、审批暂停与真正失败；审批和用户取消不是 ERROR。连续领取失败使用已有重试周期，并对同类重复日志限频。

## 5. 历史压缩：覆盖直接调用模型的路径

替换 `SummaryCompaction.Summarize` 内现有 Generate 调用片段：

```go
logger := logging.GetLogger(ctx).With(
    "component", "conversation",
    "operation", "conversation.compact",
)
startedAt := time.Now()
logger.InfoContext(ctx, "context compaction started",
    "compacted_message_count", compactedmsgcnt,
    "retained_message_count", len(messages)-compactedmsgcnt,
)

request = agentmodel.PatchDanglingToolCalls(request)
response, err := compactor.Model.Generate(ctx, agentmodel.ToEinoMessages(request))
if err != nil {
    logger.ErrorContext(ctx, "context compaction failed",
        "duration_ms", time.Since(startedAt).Milliseconds(),
        "error", err,
    )
    return nil, 0, err
}
```

摘要空值和空文本检查通过后，再写 `context compaction finished`。不在每个 token 上写日志。

## 6. 其余接入位置

| 现有位置 | 记录 | 保持的边界 |
|---|---|---|
| `host/web` HTTP Handler | request_id、路由、状态码、耗时 | 包装 ResponseWriter 时保留 Flush/Unwrap，SSE 实测 |
| `manager` Submit / AckInput / SaveOutput | message_id、thread_id、run_id、错误、耗时 | MySQL 已提交但 Redis 发布失败必须分开说明 |
| `worker` 领取/续租/释放 | 领取成功、续租失败、关闭、释放失败 | 正常空轮询和每次续租成功只记 DEBUG |
| `run.Run.Execute` | started / blocked / resumed / canceled / finished / failed | 日志不决定或修改 Run 状态 |
| `graph/execution/model.go` | model_name、用途、message_count、耗时、用量、错误 | 流式请求读到 EOF 才完成；Recv 错误也记录 |
| `graph/execution/tool_executor.go` | tool_name、tool_call_id、耗时、退出码、错误 | eager/普通工具在共用的执行边界记录；审批不算执行 |
| `graph/execution/subagent.go` | graph_run_id、agent_name、depth、parent_tool_call_id | 目前子图清空 Callbacks，不能只依赖父图回调 |
| `graph/checkpoint` | checkpoint_id、save/load、耗时、错误 | 记录结果，不记录 checkpoint 内容 |
| `localmodel` | training_job_id、进程 PID、日志路径、状态、退出码 | 原始 stdout/stderr 保留为原始文本 |

Manager 错误记录在操作边界；DAO 只补慢查询/数据库错误，默认不打印带参数 SQL。全量消息、工具入参/输出、API Key、DSN、截图不进入常规日志。模型错误保留经过敏感字段清理和长度限制的错误说明。

```json
{
  "time": "2026-10-11T13:02:12+08:00",
  "level": "ERROR",
  "msg": "context compaction failed",
  "service": "worker",
  "component": "conversation",
  "thread_id": "2000000000000013074",
  "run_id": "example-run",
  "operation": "conversation.compact",
  "duration_ms": 921,
  "error": "HTTP 400: tool call has no matching tool result"
}
```

## 7. 本地部署代码

`deploy/logging/compose.yaml`：版本是固定示例，部署前校验 ARM64 镜像和配置；不是版本升级建议。

```yaml
name: deepagent-logging
services:
  loki:
    image: grafana/loki:3.7.0
    command: ["-config.file=/etc/loki/config.yaml"]
    ports: ["127.0.0.1:3100:3100"]
    volumes:
      - ./loki.yaml:/etc/loki/config.yaml:ro
      - loki-data:/loki
    restart: unless-stopped
    logging: &container-logging
      driver: json-file
      options: {max-size: "10m", max-file: "3"}

  alloy:
    image: grafana/alloy:v1.14.1
    command: ["run", "--storage.path=/var/lib/alloy", "/etc/alloy/config.alloy"]
    user: "0:0" # 读取宿主机 0700 日志目录；只读挂载。
    volumes:
      - ./alloy.alloy:/etc/alloy/config.alloy:ro
      - ../../.eino-cli/logs:/logs:ro
      - ../../.eino-cli/local-model:/local-model:ro
      - alloy-data:/var/lib/alloy
    restart: unless-stopped
    logging: *container-logging

  grafana:
    image: grafana/grafana:12.3.0
    ports: ["127.0.0.1:3000:3000"]
    environment:
      GF_AUTH_ANONYMOUS_ENABLED: "true"
      GF_AUTH_ANONYMOUS_ORG_ROLE: Editor # 本机单用户可打开 Explore、自定义查询。
      GF_USERS_ALLOW_SIGN_UP: "false"
    volumes:
      - ./grafana/provisioning:/etc/grafana/provisioning:ro
      - ./grafana/dashboards:/var/lib/grafana/dashboards:ro
      - grafana-data:/var/lib/grafana
    restart: unless-stopped
    logging: *container-logging

volumes:
  loki-data:
  alloy-data:
  grafana-data:
```

`/local-model` 挂载源以配置中的 DataDirectory 为准，示例为仓库内目录。三个服务只通过 Compose 内部网络通信，对外端口只绑定本机。

Mac 启动脚本捕获 stderr，使用本机已安装的 `/usr/sbin/rotatelogs`；`-L` 给采集器提供固定路径，`-n` 限制文件数量。常规日志只写 JSON 文件，stderr 留给原始输出、panic 和写入失败时的兜底：

```bash
umask 077
mkdir -p .eino-cli/logs
.eino-cli/bin/deepagent-worker --config yaml/deepagent.yaml \
  2> >(/usr/sbin/rotatelogs -f -n 5 \
    -L .eino-cli/logs/worker.stderr.log \
    .eino-cli/logs/worker.stderr 10M)
```

Web 启动脚本同样处理。MLX 现有原始日志沿用现有路径，输出改接同一滚动库；rolling writer 随对应进程 Wait/清理关闭，不在 Start 后立即关闭。仍不承诺崩溃日志零丢失。

`deploy/logging/alloy.alloy`：

```alloy
loki.source.file "agent" {
  targets = [
    {__path__ = "/logs/web.jsonl", service = "web"},
    {__path__ = "/logs/worker.jsonl", service = "worker"},
  ]
  forward_to = [loki.process.agent.receiver]
}

loki.process "agent" {
  stage.json {
    expressions = {level = "level", timestamp = "time"}
  }
  stage.timestamp {
    source = "timestamp"
    format = "RFC3339Nano"
  }
  stage.labels {
    values = {level = ""}
  }
  forward_to = [loki.write.local.receiver]
}

// 子进程是原始文本，不强制解析成 Go JSON。
local.file_match "mlx" {
  path_targets = [
    {__path__ = "/local-model/server.log", service = "mlx", component = "inference"},
    {__path__ = "/local-model/jobs/*/train.log", service = "mlx", component = "training"},
  ]
}

loki.source.file "mlx" {
  targets = local.file_match.mlx.targets
  forward_to = [loki.write.local.receiver]
}

loki.source.file "stderr" {
  targets = [
    {__path__ = "/logs/web.stderr.log", service = "web", component = "stderr"},
    {__path__ = "/logs/worker.stderr.log", service = "worker", component = "stderr"},
  ]
  forward_to = [loki.write.local.receiver]
}

loki.write "local" {
  endpoint {
    url = "http://loki:3100/loki/api/v1/push"
  }
}
```

仅 service、level 和有限的原始日志 component 进入标签。任务、Run、训练任务 ID 留在 JSON 或路径字段里。原始训练日志通过 lifecycle 日志中的 training_job_id / 日志路径定位；共享 MLX 推理进程原始输出不能伪造为某个 Run 的日志。

`deploy/logging/loki.yaml`：

```yaml
auth_enabled: false
server:
  http_listen_port: 3100
common:
  path_prefix: /loki
  replication_factor: 1
  ring:
    kvstore:
      store: inmemory
  storage:
    filesystem:
      chunks_directory: /loki/chunks
      rules_directory: /loki/rules
schema_config:
  configs:
    - from: 2024-01-01
      store: tsdb
      object_store: filesystem
      schema: v13
      index:
        prefix: index_
        period: 24h
compactor:
  working_directory: /loki/compactor
  retention_enabled: true
  delete_request_store: filesystem
limits_config:
  retention_period: 168h
  reject_old_samples_max_age: 168h
analytics:
  reporting_enabled: false
```

`deploy/logging/grafana/provisioning/datasources/loki.yaml`：

```yaml
apiVersion: 1
datasources:
  - name: DeepAgent Logs
    uid: deepagent-loki
    type: loki
    access: proxy
    url: http://loki:3100
    isDefault: true
    editable: false
```

Dashboard 使用固定 UID `deepagent-logs`，预设任务执行、模型与工具、错误、训练四类面板。任务级入口直接打开 Explore，避免为任务 ID 再实现一套 Dashboard 变量协议。

## 8. 查询与 Agent 页面入口

```logql
# 当前任务的所有 JSON 日志
{service=~"web|worker"} | json | thread_id="2000000000000013074"

# 某次 Run
{service="worker"} | json | run_id="example-run"

# 最近错误
{service=~"web|worker", level="ERROR"}

# 历史压缩失败
{service="worker", level="ERROR"} | json | operation="conversation.compact"

# 训练原始输出
{service="mlx", component="training"}
```

任务菜单的“查看日志”只打开链接，不新增后端日志查询接口：

```javascript
function openTaskLogs(threadID, runID = '') {
  let query = '{service=~"web|worker"} | json';
  query += ' | thread_id=' + JSON.stringify(String(threadID));
  if (runID) query += ' | run_id=' + JSON.stringify(String(runID));
  const datasource = {type: 'loki', uid: 'deepagent-loki'};
  const panes = {logs: {
    datasource: datasource.uid,
    queries: [{refId: 'A', datasource, expr: query, queryType: 'range', editorMode: 'code'}],
    range: {from: 'now-24h', to: 'now'},
  }};
  const url = new URL('http://127.0.0.1:3000/explore');
  url.searchParams.set('schemaVersion', '1');
  url.searchParams.set('orgId', '1');
  url.searchParams.set('panes', JSON.stringify(panes));
  window.open(url, '_blank', 'noopener');
}
```

“全部任务日志”不传 run_id；失败提示旁的入口传该次 run_id。中英文文案复用现有 i18n。

## 9. 故障边界与验收

| 情况 | 设计行为 / 验收 |
|---|---|
| Loki / Grafana 停止 | Agent 继续执行，本地文件继续写；Grafana 不能查时可直接打开文件 |
| Alloy 正常重启 | positions 持久化到 alloy-data；从保存位置继续读取当前文件 |
| Loki 长时间不可用 / Alloy 异常退出 | 允许集中日志缺口，原始文件保留到滚动清理；不承诺无丢失补采 |
| Alloy 停止期间日志轮转 | 旧文件仍在本地保留范围内，但不会自动扫描备份重复导入；手动定位旧文件 |
| 日志磁盘写入失败 | stderr 保留该条日志；不向业务返回日志错误；磁盘 I/O 本身仍有耗时 |
| Worker / Web 崩溃 | 已落盘日志可查询；panic / 启动错误从滚动 stderr 文件查询，不能保证崩溃前最后一条落盘 |
| 实际模型对话、工具调用、审批恢复 | 对应任务/Run 能查到各阶段，正常审批/取消不记 ERROR |
| 人工构造模型 HTTP 400 | 用独立测试模型端点触发，能区分普通模型调用和 conversation.compact |
| 隔离配置中的 MySQL / Redis 断连 | 查到领取、续租、保存等实际失败操作；不故意破坏开发库 |
| SSE / 子代理 | 流式输出不受 HTTP 日志包装影响，子图日志带父任务和执行字段 |
| 日志轮转 / 重启 | 旋转期间 Agent 正常工作，验证本地文件数量和保留配置 |
| 本地 MLX 推理与训练 | 查到 lifecycle、job ID、退出码，并能打开关联原始日志 |

## 10. 参考

- [Loki 架构](https://grafana.com/docs/loki/latest/get-started/overview/)
- [Loki 标签](https://grafana.com/docs/loki/latest/get-started/labels/)
- [Loki 保留策略](https://grafana.com/docs/loki/latest/operations/storage/retention/)
- [Alloy 文件采集](https://grafana.com/docs/alloy/latest/reference/components/loki/loki.source.file/)
- [Alloy 发送与重试](https://grafana.com/docs/alloy/latest/reference/components/loki/loki.write/)
- [Grafana provisioning](https://grafana.com/docs/grafana/latest/administration/provisioning/)
- [Grafana Explore URL](https://grafana.com/docs/grafana/latest/visualizations/explore/get-started-with-explore/)
- [lumberjack v2.2.1](https://github.com/natefinch/lumberjack/tree/v2.2.1)
