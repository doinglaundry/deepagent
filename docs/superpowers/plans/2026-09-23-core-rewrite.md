# Core 重构实施清单

> 依据：用户提供的 50 节最终技术方案。已授权直接实施；在当前工作区保留已有暂存改动。

## 完成目标

```text
ThreadHost → DeepAgentThread → Run → DeepAgent → Eino Graph
Conversation = History + Compact + Usage
Registry = 工具定义；ToolExecutor = 单一执行账本
RunState = 唯一 Graph local state / checkpoint 状态
```

不引入 Definition、Kernel、RunDeps、Request wrapper 或新 pipeline。外部 ThreadHost 协议、公开 API、事件具体类型、历史与 checkpoint 兼容性均为迁移契约。

## 执行任务

- [ ] 1. 契约基线：恢复当前构建，固定 API、事件、历史、工具与 ThreadHost 行为测试。
- [ ] 2. 最短执行链：core/graph 实现 DeepAgent；prepare/model/tools/continue/finish 节点；Run/Stream 共用 execute。
- [ ] 3. core/runtime/agentthread：Run 生命周期、pending、输入归属、结束边界、取消与事件屏障。
- [ ] 4. core/internal/conversation：持久化先行、去重、版本化压缩、完整恢复、唯一 usage。
- [ ] 5. core/runtime/checkpointer：Eino snapshot envelope、身份/版本校验、原 interrupt 点恢复。
- [ ] 6. core/tools 与 graph/tools：Registry、Policy、Approval、Parallel、Eager、ReturnDirect、执行去重。
- [ ] 7. core/middleware：保留公共接口，能力检测，顺序与每 Run 可变状态隔离。
- [ ] 8. ChildRunner、Memory、MCP、ModelHub：接入唯一 Graph，清理生命周期。
- [ ] 9. ThreadHost 验收：Post→Ack；Event→SaveOutput→Yield→Release；Block/Resume/Lease/Close。
- [ ] 10. 契约通过后删除旧 engine/ADK/重复 Graph 与执行代码，全量测试和 race 检查。

## 验证

使用用户第 49 节的 29 个行为测试作为验收清单；外部服务用边界 fake，Graph、Conversation、Registry、Executor 与线程使用真实实现。每项记录失败原因、实现与通过命令，不将 sandbox 权限错误记为源码失败。最终必须核对全部工具 schema、参数别名和旧 checkpoint fixture。
