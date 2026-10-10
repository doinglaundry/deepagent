# 本地个人模型（第一版）

```text
用户 → 现有 Eino Graph → 云端模型
                         ↓ 选择调用 ask_local_model
                       MLX 本地模型 → 工具结果 → 云端模型

明确确认问答 → MySQL 样本 → Worker 微调 → 候选 Adapter
```

没有新 Graph 节点、模型 Router 或第二套 Agent 循环。本地模型只回答一次；训练不作为工具暴露给模型。

## 安装与配置

Apple Silicon Mac，Python 3.11，模型使用本地快照。示例用较小的 Qwen3 验证链路，不代表它已经理解个人偏好。

```bash
python3.11 -m venv .eino-cli/mlx
.eino-cli/mlx/bin/pip install 'mlx==0.30.4' 'mlx-lm[train]==0.31.1'
.eino-cli/mlx/bin/hf download mlx-community/Qwen3-0.6B-4bit --local-dir .eino-cli/models/qwen3
```

这里固定了实测可用的依赖组合，避免 [MLX GPU stream 的线程兼容问题](https://github.com/ml-explore/mlx-lm/issues/1181)。

在现有 YAML 中添加以下配置；原 `default_model` 继续使用 API 模型。

```yaml
local_model:
  name: my-personal-model          # 样本和作业的数据库隔离标识
  base_model_parameters_path: /absolute/path/.eino-cli/models/qwen3  # 原模型参数目录
  python: /absolute/path/.eino-cli/mlx/bin/python
  data_directory: /absolute/path/.eino-cli/personal-model  # 日志、训练数据和候选参数
  port: 18080
  iterations: 100
  auto_train: false
  # fine_tuned_parameters_path: /absolute/path/to/validated/adapter
```

重新启动 Web 和 Worker 即可启用。`data_directory` 必须在模型和 Adapter 目录之外。一个 Mac 用户同时只启动一个 MLX Worker；推理和训练共享设备锁。训练期间用户任务继续使用 API 模型；本地工具正忙时立即返回提示，不等待，也不取消训练。Worker 崩溃时，仍存活的子进程继续持锁；先停止该进程，再重启 Worker。

## 确认样本和查看训练记录

在已完成的回复下点击 **用于训练**，右侧工作记录会展示原问题和训练回答。可以包含上一轮问答，也可以修正回答副本，确认后才保存。原始对话不会被改动，历史压缩也不会影响已保存样本。

工作记录右上角的 **训练数据** 可查看当前任务的标记、新增样本数和最近训练记录。回复下可以取消标记；取消只影响后续导出的训练数据，不会撤销已开始的训练或回退已经生成的参数。界面支持中文和英文。

只接收已完成执行中的纯文本问答，不收集工具结果、图片或任意电脑文件。同内容去重，但分别保留消息来源；取消一个来源不会移除其他消息的授权。

接口使用字符串消息 ID，服务端从原始收发记录读取问题，页面只提交训练回答副本：

```text
GET    /api/local-model/status
GET    /api/local-model/examples
GET    /api/local-model/examples?thread_id=...&message_id=...&include_previous=true
POST   /api/local-model/examples
       {"thread_id":"...","message_id":"...","include_previous":false,
        "answer":"用于训练的回答","confirmed":true}
DELETE /api/local-model/examples?thread_id=...&message_id=...
```

自动训练需要新增至少 100 条确认样本，且 train/valid/test 三个分区均有数据；数据分割失败时需要继续增加样本。同一首问的多个确认快照始终归到同一分区，避免训练与验证互相泄漏。

作业状态：`running → pending_validation / failed / canceled`。重启后遗留的 running 标为 failed，不会自动重跑。日志和数据保存在 `data_directory/jobs/<job-id>/`。

## 什么时候自动训练

只有 `auto_train: true` 才启用。条件：上次训练尝试之后有至少 100 个新确认样本、接电、十分钟没有任务、当前没有执行中的 Thread。Worker 每五秒检查一次，满足条件后直接创建 running 记录并训练，不提供手动触发接口或待执行队列。失败或取消后，需在本次尝试之后再新增至少 100 条确认样本，才会触发下一次训练。

这是保守的调度门槛，不代表一百条样本就能获得有效个性化。

## 候选模型如何使用

训练采用 MLX QLoRA，并输出留出集 loss；`pending_validation`（待验证）表示训练进程完成、微调参数文件存在，仍需验证回答质量。当前模型不会被自动替换。

先用独立评估问题比较基础模型与候选 Adapter 的回答。确认效果后，等待已有 Run 完成，再将 `fine_tuned_parameters_path` 指向候选目录，重启 Worker。不要覆盖运行中的基础模型或 Adapter 文件。

RunState.LocalModelParametersFingerprint 保存原模型与微调参数的内容指纹。恢复发现指纹不一致会拒绝执行并保留 checkpoint；恢复原配置后可继续原 Run。

实现入口：`graph/tools/local_model.go`（工具）、`localmodel/service.go`（推理与设备生命周期）、`localmodel/training.go`（调度与微调）、`dal/db/local_model.go`（存储）。

## 本机验收记录（2026-10-10）

以下为删除手动触发链路前的验收记录；本次消息标记与自动训练验收见下节。

Apple M1 Pro，Qwen3-0.6B-4bit，以上固定依赖组合：

- 真实云端模型通过现有 Graph 调用本地模型，收到 `LOCAL_READY` 后生成最终回复。
- HTTP 确认 40 条合成样本；真实 QLoRA 训练一步，生成 2,889,527 字节的候选 Adapter，并执行 valid/test loss 计算。
- 训练期间本地工具立即返回忙碌提示，用户任务不会取消训练；训练结束后基础模型仍正常回答；显式加载候选 Adapter 后收到 `LOCAL_ADAPTER_READY`。
- 回归覆盖重复样本、数据隔离、进程崩溃后的设备锁、请求取消、checkpoint 版本不一致拒绝恢复及恢复原版本后继续执行。

这是流程验收；合成样本和一步训练没有证明个性化质量提升。第一版不自动收集电脑文件。


### 消息标记与自动训练页面验收

2026-10-10 使用 Chrome、独立 Web/Worker、隔离 MySQL/Redis 和真实 API 模型：预览原问答、包含前一轮、修改训练副本、确认、刷新恢复、英文切换和取消标记均通过，原始对话不变。

验收构建临时设为 10 条确认样本和 10 秒空闲：9 条不启动，10 条由原自动调度器触发真实 QLoRA 训练一步，train/valid/test 为 8/1/1，生成 2,889,527 字节参数并进入 `pending_validation`。训练期间 API 对话继续完成；取消标记不会删除已生成参数。

本机无法接电，先确认电池供电会阻止启动，再仅在验收构建模拟接电完成训练。正式代码仍要求 100 条新样本、空闲 10 分钟且接电；没有新增手动训练入口。上述一步训练只验证流程，不代表个性化质量提升。
