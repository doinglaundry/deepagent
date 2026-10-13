# 联网搜索与网页读取

DeepAgent 的主模型负责分析；`web_search` 搜索链接，`read_url` 读取正文。模型 API 本身不提供这些联网能力。

## 不使用搜索密钥

本地配置只需开启：

```yaml
web:
  enabled: true
```

主 Agent 的 `web_search` 会使用可见 Chrome 打开 Bing；`read_url` 优先读取 HTTP 正文，识别到验证页时转入同一个 Thread 的 Chrome。无需启用 Mac 桌面工具，也无需辅助功能权限。

遇到验证时 Run 保存 checkpoint 并暂停。请在 **DeepAgent 打开的 Chrome** 中完成验证码或登录，再点击网页中的 **“已完成验证，继续”**。工具读取该 Chrome 当前页面，不重新导航，也不把 cookie 复制给 HTTP 客户端。“停止读取”会结束当前网页读取；验证仍未完成则继续等待。

等待期间刷新 DeepAgent 网页不会丢失问题。浏览器暂存五分钟；浏览器被关闭、过期或 Worker 重启后，旧验证会话不能续用，需要重新打开页面验证。只读取原网站，手动切到其他网站会被拒绝。子代理不能接管浏览器。

人工等待不计入联网预算。网站可能仍拒绝自动化浏览器；本功能不自动求解验证码，也不能保证访问需要账户、积分或付费的内容。

## 可选：配置搜索 API

当前代码支持返回 JSON 的 GET 搜索 API，例如 [Brave Search API](https://api-dashboard.search.brave.com/app/documentation/web-search)：

```yaml
web:
  enabled: true
  search_url: https://api.search.brave.com/res/v1/web/search
  headers:
    X-Subscription-Token: "${BRAVE_SEARCH_API_KEY}"
```

启动 Worker 的环境需要设置 `BRAVE_SEARCH_API_KEY`，也可以将凭据写入被 Git 忽略的本地配置。不要把真实密钥写入示例文件。修改配置后重启 Worker。

配置 `search_url` 后 `web_search` 使用该 API；不配置时通过 Chrome 搜索。API 或搜索页面返回的摘要都不等于已读取原文。

## 为什么拿不到内容

2026-10-13 的本机 curl 与匿名 Chrome 对照检查发现：

| 现象 | 实际含义 | 处理 |
| --- | --- | --- |
| 一亩三分地返回 403、`cf-mitigated: challenge` | Cloudflare 验证页，未取得正文 | 在用户授权的浏览器中完成验证后读取，或请用户提供内容 |
| Google HTTP 200 后进入 `/sorry/index` | 异常流量验证，不是搜索结果 | 暂停后由用户在 Chrome 验证，或配置搜索 API |
| 旧 Shell 脚本拿到百度、搜狗 302 | 脚本未跟随重定向，解析了跳转页 | `read_url` 跟随重定向并返回最终 URL |
| Bing、360 等返回 200，但没有目标资料 | 请求成功，搜索结果不相关 | 核对来源与主题，不把公司首页当作面经 |
| Internet Archive 返回 `[]` | 这次查询没有归档命中 | 报告没有结果；不代表网络故障，也不能证明资料不存在 |

网页工具保留正文中的 HTTP(S) 来源链接，并识别已确认的 Cloudflare、Google 和 Brave 验证提示。普通文章提到 captcha 不会因此被拒绝。更多网站的验证形式仍可能需要补充识别。

浏览器能够运行 JavaScript，但不能保证通过网站验证。DeepAgent 使用独立 Chrome，不接管用户已经打开的个人 Chrome；HTTP 工具与 Chrome 不共享登录状态。验证成功后必须读取原来的 Chrome 页面。

## 搜索限制

- 每个网页请求最多 20 秒，每个 Run 累计请求时间最多 2 分钟；不包含模型思考或审批等待。
- 连续失败 3 次，或联网预算用尽，下次模型调用只整理已有结果，不再调用工具。
- 预算随 checkpoint 保存；新 Run 使用新预算。
- 接近原有模型调用上限时，最后一次调用用于收尾。
- 编译、训练和后台命令保持原有时间限制。Shell 的联网行为目前通过模型指令约束，这不是操作系统层面的网络隔离。

## 本地验收（2026-10-13）

使用可见 Chrome 155、真实模型、Web、Worker、MySQL 和 Redis 验证：

- 受控验证页自动暂停；刷新和语言切换后仍可继续原 Run。
- 未完成验证就点击继续会再次暂停；完成后读取同一 Chrome 的页面和 cookie，不重新发 HTTP 请求。
- 取消读取不重试；Worker 重启后明确报告旧验证会话过期。
- 真实 Bing 无密钥搜索返回 Go 官方链接，已处理初次渲染等待和编码跳转链接。
- 跨网站读取、子代理浏览器接管、损坏链接及来源链接转换有回归测试。
- 69 项前端测试、全仓新竞态测试（`-count=1 -p=1`）通过。

并行启动全仓 Chrome 测试和浏览器验收时曾出现一次 `TestWebBrowserVerificationResumesSamePageWithoutRepeatingHTTP/still_blocked` Chrome 启动超时，分开运行后完整复测通过；失败记录保留在本地验收目录。人工验证流程使用本机受控页面测试，没有自动求解真实网站验证码。
