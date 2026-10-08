# Computer Use Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement task-by-task. The user explicitly requested immediate inline implementation; no further implementation authorization is needed.

**Goal:** 浏览器和 Mac 应用都通过当前 Eino Graph 可观察、可操作、可审批和可取消。
**Architecture:** Browser 属于 Thread，Desktop 属于 Worker。工具直接调用两个真实资源，Policy 是唯一网站/应用授权来源；既有 RunFactory/ResourceCloser 管理 Run 桌面占用和图像请求裁剪。
**Tech Stack:** Go 1.25、当前 Eino、chromedp 固定兼容版本、Swift/Apple 系统 API。
**Spec:** docs/superpowers/specs/2026-10-08-computer-use-design.md

## Global Constraints

- 不新增 Agent 循环、Graph 节点、工具协议、Config 包装层或权限框架。
- 禁止 Go if 初始化语句。方法动词开头，常规排版，明确变量名。
- 新构造器不接收 helperPath、allowedOrigins、allowedApps；固定辅助程序位置。
- 当前任务按工具记忆授权；网站/应用检查优先于放行；所有电脑工具禁止 eager 与并行。
- 当前浏览器/窗口实物不进入 checkpoint；过期观察禁止动作；未知副作用禁止重放。
- 最大图像边 1280；较早电脑截图只从模型请求移除，持久化历史保留。
- Mac 专用、默认禁用；真实视觉模型和系统权限不具备时明确保留未通过项。

## Review Focus

- 授权绕过：重定向、当前页面变化、始终允许仍必须检查目标；Task 3 固定测试。
- 恢复死循环：审批后界面未变应执行一次，界面变了应拒绝；Task 1/2/4 实测。
- 争用与取消：多进程锁、取消读写、helper 死亡不遗留资源；Task 2 实测。
- 多模态丢失：图片从 Eino 工具经过历史、事件到模型与 UI；Task 3/4 测试。
- 图像污染：裁剪不能修改原消息或其他类型图像；Task 3 测试。

### Task 1: Browser

**Files:** computer/browser.go、computer/browser_test.go、go.mod/go.sum。
**Interfaces:** NewBrowser(ctx, profileDir)；PerformAction(ctx, operation, Action) 返回 Observation；Close(ctx)。Action 是实际工具/系统动作的序列化参数，Observation 是截图和元素快照，不是执行包装层。

- [x] 在 browser_test.go 编写真实 httptest 页面用例：输入、点击、滚动、截图、过期观察拒绝；先运行确认实现缺失。
- [x] 固定安装 Go 1.25 兼容的 chromedp；实现独立 Chrome profile、请求取消、DOM 元素标识与操作前位置/身份核对。
- [x] 像素最大边 1280；只返回经过原授权 origin 的页面内容，跨 origin 返回重新观察提示。
- [x] 运行浏览器测试；浏览器退出且 profile 锁释放；记录结果；源码暂未提交。

### Task 2: Mac Desktop

**Files:** computer/desktop.go、computer/native/main.swift、computer/desktop_test.go、scripts/build-computer.sh。
**Interfaces:** NewDesktop(ctx)；PerformAction(ctx, ownerRunID, operation, Action) 返回 Observation；Close(ctx)。释放桌面占用直接调用 PerformAction 的 release 操作，共用 Task 1 的实际动作/观察数据。

- [x] 测试固定辅助程序定位、错误/取消与两个 owner 的占用；缺少实现先失败。
- [x] Swift 实现本地 JSON 通信、目标窗口截图/AX 元素、键盘鼠标动作、Retina 转换、本机 flock、权限检查。
- [x] Go 实现串行子进程通信、超时后清理与下一次重建；helper 重建后的观察 ID 失效。
- [x] 构建脚本将 Worker 与 deepagent-computer 放在同一输出目录。
- [ ] 实际操作 TextEdit；取消、关闭和跨 helper 争用验证，无权限明确返回错误；记录结果；源码暂未提交。

### Task 3: Eino Tools, Policy and Lifecycle

**Files:** tools/computer.go、tools/computer_test.go、middleware/computer.go、middleware/computer_test.go、threadhost/thread.go、threadhost/computer.go、threadhost/computer_test.go。
**Interfaces:** NewBrowserTools(*Browser)、NewComputerTools(*Desktop) 返回 []ToolDescriptor；NewComputer(*Desktop) 返回现有 Middleware；现有 PolicyFunc 调用唯一目标检查。

- [x] 测试 12 个工具名称/schema、只读/审批/并行属性、图片结果、子代理拒绝、目标名单先于始终允许；先确认失败。
- [x] 一个 EnhancedInvokableTool 实现复用两组工具 schema/解码/结果转换，直接调用真实操作；不增加协议。
- [x] Policy 检查 browser_open URL 或当前 URL，检查每个 Mac 调用的 bundle ID；拒绝无效 URL/越界/缺少目标。构造器无名单，资源层只做界面身份核对。
- [x] RunFactory 绑定桌面 owner，ResourceCloser 在工具取消后释放；请求副本只保留最近电脑工具截图，原历史和非电脑图像不变。
- [x] ThreadHost 独立 Chrome profile + CloseResources，默认子代理 ToolMask 排除电脑工具。
- [x] 跑工具、middleware、Policy 测试与竞态检查；记录结果；源码暂未提交。

### Task 4: Worker/UI and Real Acceptance

**Files:** appconfig/config.go、worker/app.go、yaml/deepagent.example.yaml、protocol/event/event.go、thread/events.go、host/web/app.js/app.css/i18n.js、相关既有测试、README.md。
**Interfaces:** 三项新配置；工具完成事件 parts 使用当前 MessagePart，不定义第二套图片事件。

- [x] 测试禁用默认、启用时名单校验、图片事件与 UI 展示，先确认失败。
- [x] Worker 创建/关闭 Desktop，ThreadHost 接入工具；配置名单不进入 Graph Config。
- [x] 现有工具页展示截图，对话用简短浏览/桌面状态，保留中英切换和审批。
- [x] go build ./...；go test -race ./...；node --test deepagent/host/web/app.test.cjs。
- [ ] 启动本地真实服务；真实模型读取截图后分别完成浏览器、Mac 任务；人工/自动检查外部实际结果。
- [x] 审批拒绝无动作，允许执行一次，暂停恢复和重启过期观察不执行；多 Worker 互斥和停止释放验证。
- [x] 独立审查完成代码；修正重要问题并补必要复测。报告具体通过项/阻塞项和新增非测试业务行数。

## 实际验收记录

- 浏览器：真实 Chrome 打开、输入、点击、滚动、取消、过期观察及覆盖层拒绝通过。
- 真实模型：沿用本地 DeepSeek，读取 canvas 截图中的 5837；没有文字替代。
- 服务：独立 Web + Worker + 临时 MySQL/Redis；输入审批暂停时重启 Worker，原 Run 恢复，旧观察拒绝。重新打开后输入 5837，提交副作用恰好一次，四张工具截图持久化。
- Mac：Swift 构建、通信取消/重建、损坏响应及结果未知分类、两个原生进程争用/崩溃释放通过。TextEdit 的打开、输入、截图和焦点变更实测未通过验收，辅助功能仍未授权；没有宣称桌面实机可用。
- 前端：40 项 JS 测试通过；工具截图只在工具页展示。
- 全仓：构建、竞态测试通过；最后未知结果修复后再次全仓回归。
- 独立审查：覆盖层点击、键盘焦点/选择、阻塞浏览器无限保留三项已处理。覆盖层和保留期限测试 RED→GREEN；Mac 焦点变更实测待权限。另补损坏回复测试 RED→GREEN。
- 本轮只启停临时验收服务；保留证据 /private/tmp/deepagent-computer-use/service-acceptance.json，未改开发数据库。
- Computer Use 源码尚未提交；conversation 改动已单独提交。

## 实施边界

1. 按已批准方案直接实施；使用现有功能分支和原目录，保留并发改动。
2. 工具、Graph 和审批仍是一条执行路径；动作和观察共享一套实际数据结构。
3. Chrome 按 Thread 保留审批现场，最多五分钟；重启或跨 Worker 不承诺登录和页面恢复，必须重新观察。
4. Desktop 构造只检查 helper 就绪，Mac 动作再检查系统权限，避免影响浏览器启动。
5. 仅裁剪模型请求副本中的旧电脑截图，历史不变；文本与图片通过现有消息和事件传递。
6. Desktop 锁通过既有 ResourceCloser 在工具取消后释放，未知结果不重放。


## 用户确认后的逻辑精简

- YAML 的 computer_enabled 为唯一开关，ThreadHost 根据已创建的 Desktop 装配能力，不重复保存启用状态。
- 工具结果在写入历史时填写 ToolName；截图中间件倒序遍历一次，只从模型请求中移除旧截图。Transcript 同步使用工具名，避免重复记录。
- 浏览器各动作只准备 chromedp.Action，共用一次执行；Desktop 的写入、读取、解码失败共用一条取消清理路径。
- 删除 Browser 工厂转发闭包和 Desktop.ReleaseRun，未新增结构体。
- 本轮非测试 Go 文件按普通排版计数：1486 → 1451，净减少 35 行。
- 最终 go build ./...、go test -race ./...、40 项前端测试与 12 个改动 Go 文件的 if 初始化语句检查均通过。
- 最新真实模型 + Graph + Chrome 验收：截图读出 5837，三次审批分别恢复原 Graph，输入并点击后服务器仅收到一次提交。证据：/private/tmp/deepagent-computer-use/simplify-graph-acceptance.json。
- Mac TextEdit 实机验收仍待系统辅助功能授权；本轮未声称已通过桌面实机验收。
