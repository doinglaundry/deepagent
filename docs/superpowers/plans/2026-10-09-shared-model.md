# Shared Model Implementation Plan

**Goal:** 公共接口与跨包数据类型集中到 `deepagent/model`，实现对象留在所属模块，调用方直接使用新定义。

**Architecture:** 所有实现包依赖 model；model 不依赖仓库内的实现包。迁移类型附带的必要校验、序列化方法，保留 JSON、数据库表、checkpoint 注册名称和执行行为。

**Tech Stack:** Go、Eino、GORM、Redis。

## Constraints

- 不保留旧路径的转发类型别名，不新增执行层。
- 配置和有生命周期的 Manager、Worker、Thread、Run、Graph、ToolSet 留在原包。
- 包内私有接口留在使用处；所有公共命名接口集中到 model。
- Go 的赋值与 if 条件分开；不引入 if initializer。
- 工具说明缓存归 ToolSet 管理，model.ToolDescriptor 只携带共享属性。

## Tasks

- [x] 增加架构检查：公共接口只允许出现在 model，model 不导入实现包；确认现有源码会触发失败。
- [x] 按 Go 类型绑定迁移消息、Thread 记录、Run 状态、输入输出事件和辅助能力数据。
- [x] 迁移 Manager、Conversation、Filesystem、Tool、Middleware、Skill、Memory、Sandbox、Redis 的公共接口及签名依赖。
- [x] 修改全部生产与测试调用方，迁移类型测试，删除旧声明文件和别名。
- [x] 处理 ToolSet 的私有工具说明缓存，保持过滤及复制语义。
- [x] 更新 README 与源码导航，验证全仓编译、竞态测试和前端测试。

## Review Focus

- 工具属性、只读过滤和审批行为不变。
- Eino 中断续跑保留同一 RunState 与输入身份。
- 消息多模态字段及 JSON 字段不变，数据库映射不变。
- model 不能间接依赖 Manager、Graph 等实现。
- 迁移后的同名结构按用途区分，不误合并不同协议。

## Verification

- 已在迁移前运行架构检查，确认原有 27 个公共接口会触发失败；迁移后检查通过。
- 实际仓库 `go build ./...` 与 `go test -race ./...` 通过。
- 前端 `node --test deepagent/host/web/app.test.cjs`：45/45 通过。
- GORM 解析结果对比：Thread、Message 和 Run 的数据库表名、字段定义、索引一致。
- `gofmt -l deepagent cmd` 无输出，`git diff --check` 通过；无旧类型包的引用。
- 本轮未重新执行真实模型服务联调。
