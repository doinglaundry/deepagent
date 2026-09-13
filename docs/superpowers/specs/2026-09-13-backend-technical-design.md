# DeepAgent 后端方案

## 调用链

```text
UI → Manager API → Worker → Thread → Core EinoGraph
```

Manager 是统一入口；UI 不直接连接 Worker，也不持有独立调度实例。

## Manager

```text
MySQL: threads/tasks/runs/events/checkpoints
Redis: queue:tasks, lease:{taskID}, cancel:{runID}, events:{runID}
Submit → MySQL commit → Redis enqueue
Claim → atomic pop → lease → queued→leased
Event → dedupe → MySQL append → Redis publish
```

```text
event filter → MySQL event log → Redis realtime fanout
                                 └─ publish failure: warn only
```

```go
CreateThread() → MySQL thread(status=idle)
SubmitInput(threadID, message)
  → MySQL append message + create run
  → Redis enqueue runID
  → wake worker
```

空闲 Thread 不占用 Worker。只有收到首条消息或后续输入时，Manager 才创建 Run 并入队唤醒 Worker。

```go
type Session struct { SessionID string; UserID string }
type Thread struct { ThreadID, SessionID, Status string }

GetThread(threadID string) (*Thread, error)
ListThreads(sessionID string) ([]*Thread, error)
SubInput(threadID string, input []byte) error
Resume(threadID string, request *ResumeRequest) error
Cancel(threadID string, reason string) error
```

```text
SubInput
  → MySQL 保存 input
  → thread.status = runnable
  → 创建 Run
  → Redis 入队并唤醒 Worker
```

```go
type ResumeRequest struct { Input []byte }
```

```text
Resume(threadID, request)
  → GetThread(threadID)
  → status != blocked  => error
  → request.Input != nil OR Redis has queued input
       => status = ready
       => input 入 Redis queue
     otherwise
       => status = idle
  → updated_at = now
  → MySQL conditional update
```

```text
Cancel(threadID, reason)
  → GetThread(threadID)
  → status ∈ {closing, closed, blocked} => error
  → reason == "" => reason = "user cancel"
  → status = closing
  → Redis cancel:{threadID}
  → Worker cancel Thread Context
  → status = closed, canceled_at = now
```

```go
type Lease struct { TaskID, LeaseID, WorkerID string; FencingToken int64; ExpiresAt time.Time }
```

Redis TTL 租约：Worker 每 30 秒续租，2 分钟过期；写入校验 `leaseID + workerID + fencingToken`。

## Worker

```text
Claim → RunConfig → Create/Resume Thread → Thread.Run/Stream
     → Publish events → Persist result → Release lease
```

```go
type WorkerDefaults struct {
    Concurrency       int           // 8
    ScanInterval      time.Duration // 1s
    BatchSize         int           // 20
    InputBatchSize    int           // 15
    LeaseTTL          time.Duration // 60s
    InputPollInterval time.Duration // 500ms
}
```

Claim 前获取 semaphore，成功后启动 Run goroutine，结束释放槽位；扫描、领取或单任务失败只记录并继续循环。

```text
post message → success → ack(inputID, runID)
ack → accepted set + remove pending
接管 running Thread → 行锁检查 accepted → 未完成输入按原 inputID 重回 pending
```

这是允许重执行的恢复语义，不能保证外部命令、文件写入或远端 API 只执行一次。

## Thread

```go
type Thread interface {
    SubmitInput(context.Context, *schema.Message) (*RunHandle, error)
    CancelRun(context.Context, string) error
    ResumeRun(context.Context, string, ResumeOptions) (*RunHandle, error)
    History(context.Context) []*schema.Message
}
```

Thread 管历史、Run、压缩、checkpoint、中断恢复和事件记录，不访问任务队列。

## Core EinoGraph

```text
START → Model → ToolCalls? → Tools → Model
                  └──────────────→ END
```

Core 管模型、工具、中间件、GraphState、流式合并和 checkpoint，不依赖 Redis/Manager。

## 七层职责

```text
1 UI                 交互、输入、事件展示、审批
2 Manager            Thread、输入、调度、租约、事件、取消、恢复
3 Worker             领取、续租、执行、回传、收尾
4 Thread             会话历史、Run、压缩、checkpoint、恢复
5 Core EinoGraph     模型/工具循环、路由、预算、中断
6 Capabilities       文件、命令、技能、计划、协作、Hooks
7 Resource Adapters  Model、Filesystem、Shell、MySQL、Redis、MCP
```

## 状态恢复

```text
queued → leased → running → succeeded|failed|canceled
lease expired → requeue → new worker → latest checkpoint resume
```

```go
type RunEvent struct {
    EventID, TaskID, ThreadID, RunID string
    Sequence int64
    Type string
    Payload []byte
    FencingToken int64
}
```
