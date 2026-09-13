# 上下文代码录像提取记录

录像：`/Users/yingbaosun/src/LLM-source-recordings/agentthread-source-20260912.mov`，时长约 184 秒。

目标文件：

- `deepagent/core/agentthread/context_middleware.go`
- `deepagent/core/agentthread/context_mng.go`

本次对整段录像每秒抽帧，生成 184 帧并完成本机文字识别；再选取静止画面，对照先前用户提供的源码核对。原始 OCR 含识别错误，未直接当作 Go 代码写入。

识别原始数据保留在仓库外：`/Users/yingbaosun/src/LLM-source-recordings/ocr.jsonl` 和 `ocr-readable.txt`。

## 核对范围

| 文件 | 已核对的画面内容 |
| --- | --- |
| context_middleware.go | 中间件与输入排空接口、请求处理、普通与流式响应、输入拼接、压缩事件、用户输入判断、最终上下文拼接 |
| context_mng.go | 管理器字段、构建选项与初始化、历史追加与部分写入失败处理、历史读取、用量记录、压缩上下文标记、压缩快照与版本检查、恢复历史、持久化记录、阈值判断、消息 ID 与唯一键提供者 |

文件末尾已核对：第一份以 `appendModelContext` 结束，第二份以 `newHistoryRecordUniqueKey` 结束。没有发现需要新增的函数；已有源码分别包含 7 和 22 个函数/方法。

## 写入规则与限制

清晰片段与既有源码实现相符。对滚动重影、右侧截断、编辑器提示遮挡、折叠代码和不可辨认的文字，保留之前用户提供的源码，不声称由录像独立、逐字恢复。录像仍不足以证明两份源码逐字一致。

项目中保留完整可编辑实现，按用户已确认的要求使用标准库 `log/slog`，而非录像中的内部日志库。中间件多行函数签名与回调按录像中的排版整理。未因 OCR 误读修改业务行为。

这两个文件的语法和格式可单独检查；所属 agentthread 包仍缺少其他模块与依赖，不能据此宣称包编译或运行测试通过。
