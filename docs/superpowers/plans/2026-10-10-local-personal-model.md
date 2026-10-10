# 本地个人模型：最小实现计划

目标：云端 Agent 可调用本地模型；明确确认的文本问答可训练 LoRA；训练与本地推理互斥；训练期间用户任务走 API，不中断训练。候选 Adapter 不自动上线。

1. 新增普通 Eino 工具 `ask_local_model`，走现有 ToolSet / executor / checkpoint。
2. 在 model 定义 TrainingExample、TrainingJob；DAL 复用 Manager MySQL。保存完整的已确认问答快照，按内容去重。
3. 一个 localmodel.Service 管理 MLX server 和训练进程。当前 Mac 用户的设备文件锁防止多 Worker 竞争；领取 Thread 只统计活跃任务，不取消训练；本地工具遇到设备忙碌立即返回，由 API 模型继续回答。
4. Web 只提供确认样本、查询训练记录两个 API，不提供手动触发。自动训练默认关闭；启用后 Worker 仅在 100 个新样本、接电、空闲十分钟且没有执行中的 Thread 时创建 running 记录并开始训练，不使用待执行队列。
5. 数据按首问分组为 train / valid / test；最少十条，训练完成后标记为 pending_validation（待验证），不自动启用微调参数。参数指纹写入 RunState.LocalModelParametersFingerprint，恢复时校验一致。
6. 用失败测试覆盖工具调用/错误、数据分割/去重、进程互斥/取消、版本恢复，再编译全仓并跑相关竞态测试。尝试实机 MLX 推理和微调；依赖/模型缺失则明确区分验证边界。
7. 独立 agent 审查完整 diff 的过度设计与恢复/清理问题，修复后再次验证。

不新增 Router、Provider/Trainer 接口、Graph 节点、第二套 Agent loop、自动推广或前端配置面板。
