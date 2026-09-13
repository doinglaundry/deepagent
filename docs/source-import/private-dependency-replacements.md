# 内部依赖替换

当前源码不再导入 `code.byted.org` 包。替换使用项目已有 Go 版本支持的标准库与语言语法，不新增第三方依赖。

| 原依赖 | 替换方式 | 语义 |
| --- | --- | --- |
| `code.byted.org/gopkg/logs/v2` | `log/slog`，格式化消息用 `fmt.Sprintf` | 保留日志级别、上下文参数及原格式化消息；默认输出样式由 slog 决定，不复刻内部日志库的扩展能力 |
| `code.byted.org/lang/gg/choose` | `if/else` | 根据流式配置选择模型回调；仅在未指定 ContextManager 时创建默认实例 |
| `code.byted.org/lang/gg/gmap` | `maps.Keys` 配合 `slices.Sorted` | 缺少检查点存储时，将状态处理器名称排序后拼接，错误消息顺序稳定 |
| `code.byted.org/gopkg/lang/sets` | 原 map 的 `value, exists` 查询 | 按键是否存在判断，保留策略门优先于审阅编辑工具的行为 |

## 验证边界

- 全仓 Go 源码中的内部包导入已移除。
- 本次修改文件通过 Go 语法解析及 gofmt 格式检查。
- 现有 engine、engine/agentthread、types 与 Worker 测试通过；constant 包可编译，无测试文件。
- `go test -mod=readonly ./deepagent/core/...` 仍在依赖解析阶段失败：本地 middleware、tools、utils、tracing、serialiser、internal/toolerrors 等模块尚未补齐，另外 jsonrepair 与 deepcopy 尚未加入模块依赖。这些阻塞与本次移除的四类内部依赖不同，尚不能验证新 Core 的完整构建与运行行为。
