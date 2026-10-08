# DeepAgent Computer Use 最小实现设计

日期：2026-10-08。状态：待书面设计审阅，尚未实施。

## 目标

在当前 Mac 上同时操作浏览器与桌面应用。使用现有 Eino Graph、工具定义、审批、图片消息、执行账本和 checkpoint；减少手写代码与调用层级，不增加执行框架。简单指职责直接、变量明确，不压缩正常排版。

## 选择

浏览器使用 Go chromedp/CDP；Mac 使用一个 Swift 本地辅助程序，调用 ScreenCaptureKit、AXUIElement 和 CGEvent。相比增加两套 MCP 服务，这种方式无需再处理服务发现和远程协议；相比全部按截图坐标操作，浏览器与可访问的桌面元素能够直接定位。

Swift 程序只接受本地 JSON 请求并返回界面描述、操作结果和 PNG 截图，不包含模型调用和 Agent 调度。通过受控子进程的标准输入输出通信，不启动网络服务。每个请求有取消和超时，退出时释放按键、鼠标和文件锁。

## 执行路径

```text
ThreadHost → Thread → Run → 现有 Eino Graph
                             ↓
                           Model
                             ↓
                         Eino Tool
                             ↓
                 Browser 或 Mac 系统接口
                             ↓
                     操作结果与新截图
```

工具工厂返回现有 ToolDescriptor；图片工具实现 Eino EnhancedInvokableTool。工具包不依赖 middleware；middleware 不提供工具。Graph Config 不新增 ComputerConfig、ComputerRuntime、Session 或任何请求包装层。

## 新增代码边界

| 文件 | 唯一职责 |
|---|---|
| deepagent/graph/computer/browser.go | Chrome 会话、页面元素定位、浏览器动作与截图 |
| deepagent/graph/computer/desktop.go | Go 与 Swift 的通信、Run 桌面占用与观察有效性 |
| deepagent/graph/computer/native/main.swift | Mac 应用窗口、辅助功能元素、截图、键盘和鼠标；本机文件锁 |
| deepagent/graph/tools/browser.go | 浏览器 Eino 工具 schema 与直接调用 |
| deepagent/graph/tools/computer.go | 桌面 Eino 工具 schema 与直接调用 |
| deepagent/graph/middleware/computer.go | 每次 Run 的桌面资源清理与模型请求图像裁剪；不注册工具、不保存第二份 Graph 状态 |

配置和资源接入只修改现有 appconfig/config.go、worker/app.go、threadhost/thread.go 与启动/构建说明；现有 Web 静态页仅按需补动作状态和图片展示。复用既有 Graph 生命周期，不新建 Graph 节点或修改主循环。普通工具结果和图片结果继续经过当前 tools.go 与 message 转换路径。

构造入口保持直接：

```go
func NewBrowser(ctx context.Context, profileDir string, allowedOrigins []string) (*Browser, error)
func NewDesktop(helperPath string, allowedApps []string) (*Desktop, error)
func NewBrowserTools(browser *computer.Browser) []ToolDescriptor
func NewComputerTools(desktop *computer.Desktop) []ToolDescriptor
```

方法按动作命名，如 Open、Observe、Click、TypeText、PressKey、Scroll、Close。不得引入另一套 Tool.Invoke 协议。所有 Go 赋值与 if 条件分开，不使用 if 初始化语句。

## 功能与默认行为

- 浏览器工具：browser_open、browser_observe、browser_click、browser_type_text、browser_press_key、browser_scroll。
- Mac 工具：computer_open_app、computer_observe、computer_click、computer_type_text、computer_press_key、computer_scroll。
- 观察工具为只读；动作工具要求审批。全部 ParallelSafe=false，禁止 eager 执行。
- 每个动作返回当前观察结果与截图。浏览器优先页面元素；Mac 优先辅助功能元素，无法访问元素的界面才用窗口内坐标。
- appconfig 增加 computer_enabled、browser_origins、computer_apps、computer_helper 四项。默认禁用；启用时限制明确的网站 origin 和应用 bundle ID，并要求本机辅助程序存在。这些配置只用于 Worker/ThreadHost 创建资源，不扩散到 Graph Config。
- Chrome 使用每个 Thread 独立的 profile 与受控实例。第一版不接管用户已打开的个人 Chrome；可在该实例内人工登录。
- 桌面辅助程序按 Run 持有本机文件锁；多个 Worker、多个任务不得同时操作同一 Mac。ThreadHost 为默认子代理配置 ToolMask，排除 browser_ 与 computer_ 工具；工具执行入口也检查 RunState.Depth，防止通过其他子代理配置继承绕过限制。
- 浏览器归 Thread 所有，多轮 Run 复用；Thread 关闭或构造失败时关闭浏览器。Desktop 是 Worker 资源；每次 Run 的资源清理使用现有 RunFactory 与 ResourceCloser，在工具取消完成后释放该 Run 的占用。未获取占用时 Close 也是安全的。
- 临时按键/鼠标输入不会持有跨工具状态；每次操作都完成按下与释放。

## 中断、恢复与授权

审批和当前任务中按工具记住的“始终允许”使用既有实现，不合并多个动作工具。工具授权不扩大 origin 或应用范围。截图和网页/辅助功能文本属于工具观察，不注入系统指令。

观察包含 observation_id、窗口/页面身份与几何信息。后续动作引用该观察；执行前重新检查目标，页面/窗口或目标身份变化时返回 stale_observation，不执行动作，交给模型重新观察。审批恢复后重新获取占用并强制重读界面：目标身份和位置仍匹配时允许已审批动作执行一次，否则返回 stale_observation。不能仅因审批暂停就无条件拒绝原观察，避免每次授权后再次审批的死循环。Worker 重启或辅助程序重建后，旧 observation_id 一律拒绝。Retina 像素与系统坐标明确转换。

Graph checkpoint 只恢复执行状态，不承诺复原 Chrome 或 Mac 画面。Worker 重启后重建资源、重新观察，旧动作不能按旧坐标执行。Profile 可保留登录数据，不能代表恢复了原页面。沿用现有工具执行 fence；副作用结果未知时不得自动重放，禁止对动作工具透明重试。

Mac 首次启用需要系统的屏幕录制与辅助功能权限；缺少权限时返回具体错误。截图限制为目标应用/页面。图像最大边缩放到 1280 像素，并返回缩放比例；坐标操作使用截图坐标转换为对应窗口坐标。computer 中间件在 ModifyModelRequest 中复制请求消息，仅保留最近一条包含图像的 computer/browser 工具结果中的图像；较早结果保留文字。持久化历史不修改，其他类型图像不受影响，避免每轮重发全部电脑截图。

## 模型和前端

沿用现有 model.ToolCallingChatModel。启用视觉操作需要真实验证模型同时接受图片和工具调用，不能以适配器能够序列化图片代替验证。没有可用视觉模型时明确报告这一验收未完成，不静默退化或宣称整个功能通过。

沿用现有对话和工具页：对话只显示正在浏览/操作的简短状态，工具页显示动作、截图和结果。保留停止按钮与原审批交互，不重做 UI，不增加手机端。

## 验收

1. 本地浏览器测试页实际完成打开、输入、点击、按键和滚动，并检查页面结果。
2. Mac TextEdit 实际打开、输入、选择/滚动，确认辅助功能可见结果；坐标用例核对 Retina 映射。
3. 真实视觉模型读取截图，调用工具完成一项浏览器任务和一项 Mac 任务。
4. 拒绝审批不产生操作；允许只执行一次；始终允许只对当前任务同一个工具生效。
5. 审批暂停、界面变化、恢复及 Worker 重启后，旧观察不执行；不重复未知结果的副作用。
6. 两个任务/Worker 争用桌面时只有一个获得执行权；取消、超时、构造失败与关闭释放占用与子进程。
7. 编译、必要回归与竞态检查通过；记录实际新增非测试业务代码行数。

验收证据分别记录自动化检查与真实服务操作。任何因权限、视觉模型或环境阻塞的项目保持未完成状态。

## 官方参考

- chromedp：https://github.com/chromedp/chromedp
- ScreenCaptureKit：https://developer.apple.com/documentation/screencapturekit
- AXUIElement：https://developer.apple.com/documentation/applicationservices/axuielement
- CGEvent：https://developer.apple.com/documentation/coregraphics/cgevent
