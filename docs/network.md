# 联网搜索与网页读取

DeepAgent 的主模型负责分析；`web_search` 搜索链接，`read_url` 读取正文。模型 API 本身不提供这些联网能力。

## 配置搜索服务

当前代码支持返回 JSON 的 GET 搜索 API，例如 [Brave Search API](https://api-dashboard.search.brave.com/app/documentation/web-search)：

```yaml
web:
  enabled: true
  search_url: https://api.search.brave.com/res/v1/web/search
  headers:
    X-Subscription-Token: "${BRAVE_SEARCH_API_KEY}"
```

启动 Worker 的环境需要设置 `BRAVE_SEARCH_API_KEY`，也可以将凭据写入被 Git 忽略的本地配置。不要把真实密钥写入示例文件。修改配置后重启 Worker。

没有 `search_url` 时只提供 `read_url`，不能声称搜索服务已经接通。搜索 API 返回的摘要也不等于已读取原文。

## 为什么拿不到内容

2026-10-13 的本机 curl 与匿名 Chrome 对照检查发现：

| 现象 | 实际含义 | 处理 |
| --- | --- | --- |
| 一亩三分地返回 403、`cf-mitigated: challenge` | Cloudflare 验证页，未取得正文 | 在用户授权的浏览器中完成验证后读取，或请用户提供内容 |
| Google HTTP 200 后进入 `/sorry/index` | 异常流量验证，不是搜索结果 | 报告验证要求，改用正式搜索 API |
| 旧 Shell 脚本拿到百度、搜狗 302 | 脚本未跟随重定向，解析了跳转页 | `read_url` 跟随重定向并返回最终 URL |
| Bing、360 等返回 200，但没有目标资料 | 请求成功，搜索结果不相关 | 核对来源与主题，不把公司首页当作面经 |
| Internet Archive 返回 `[]` | 这次查询没有归档命中 | 报告没有结果；不代表网络故障，也不能证明资料不存在 |

网页工具保留正文中的 HTTP(S) 来源链接，并识别已确认的 Cloudflare、Google 和 Brave 验证提示。普通文章提到 captcha 不会因此被拒绝。更多网站的验证形式仍可能需要补充识别。

浏览器能够运行 JavaScript，但不能保证通过网站验证。当前 HTTP 网页工具不共享用户 Chrome 的登录状态；切换工具不会自动获得登录权限。需要验证码、账户或积分的页面，应暂停并请用户处理，或由用户提供有权访问的内容。

## 搜索限制

- 每个网页请求最多 20 秒，每个 Run 累计请求时间最多 2 分钟；不包含模型思考或审批等待。
- 连续失败 3 次，或联网预算用尽，下次模型调用只整理已有结果，不再调用工具。
- 预算随 checkpoint 保存；新 Run 使用新预算。
- 接近原有模型调用上限时，最后一次调用用于收尾。
- 编译、训练和后台命令保持原有时间限制。Shell 的联网行为目前通过模型指令约束，这不是操作系统层面的网络隔离。
