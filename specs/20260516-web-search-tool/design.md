# Web search tool — 路径 A 落地

让 lead agent 通过 LLM-自主 function_call 调用一个本地 `web_search` 工具，
所有模型走标准 `chat/completions`（兼容方舟 / OpenAI / Kimi 任意 OpenAI 协议
后端）。**不**接入 OpenAI Responses API、**不**依赖任何 server-side builtin
搜索工具。

## 总览

- **痛点**：仓库当前 `BuildBuiltinTools`（`backend/agent/tools/tools.go:19-37`）
  没有联网搜索能力；prompt 里 `directToolExamples` 提到 "ls, read_file,
  web_search, etc." 但 `web_search` 实际不存在 —— 这是一处幻觉工具引用。
- **改动范围**：新增 1 个 Go 工具实现 + 在 `Config` 上加一段 `web_search`
  yaml 配置；BuildBuiltinTools 签名从 `(root string)` 改为
  `(cfg *config.Config)`，对应 2 处调用点同步。
- **不动模型层**：`backend/agent/models.go` 不动 —— 方舟通过现有
  `Provider: "openai"` + 自定义 `BaseURL` 直接复用。
- **预期效果**：用户 prompt "北京天气怎么样？" 时 LLM 发起 function_call
  `web_search({"query": "北京天气"})`，工具回结构化结果，模型综合回答。

引用约定：本仓库代码用 `起始行:结束行:路径`；外部 API 用 `<provider>::<endpoint>`
锚点。

---

## 决策点（已拍板）

| 维度 | 选项 |
|---|---|
| 搜索后端 | **博查 Bocha**（`api.bochaai.com/v1/web-search`，`Authorization: Bearer $BOCHA_API_KEY`） |
| `max_results` 默认值 | **5** |
| `BuildBuiltinTools` 签名 | 改为 `(cfg *config.Config)` —— 单一 `config.Config` 入参，符合 AGENTS.md「Pass less data」 |

落地范围窄到只支持 Bocha 一家：`web_search.go` 里的实现与 Bocha 的请求 /
响应 schema 紧绑（`POST` `{query, count}` → `{data.webPages.value[]}`）。
未来加 Tavily / 自带后端时再在 `runBochaSearch` 旁挂一个 `runXSearch` +
`switch cfg.Provider`，不动外层文件结构。

---

## F1 · GetWebSearchTool 本地 function tool

### 目标

新增 `backend/agent/tools/web_search.go`，导出 `GetWebSearchTool(cfg
*config.Config)`，挂入 `BuildBuiltinTools`，让 LLM 通过标准 function_call
触发联网搜索。

预期效果：

- `BuildBuiltinTools` 注册 16 个工具（原 15 + 1）。
- `web_search.enabled = false`（默认）→ 工具不出现在 LLM 工具集，行为
  与现状一致。
- `web_search.enabled = true` 且 API key 已配 → LLM 看见
  `web_search(query, max_results?)` 工具，可主动调用。
- API key 未配但 enabled = true → 启动期 panic（按现有 `mustBuild`
  约定，配置错误是 startup-time bug）。

### 实现代码

#### 1. yaml shape (`backend/config/yaml.go`)

在 `Config`（`backend/config/types.go`，未在本 spec 列出，但和 Memory /
Summarization 等 sibling 字段同级）加一段 `WebSearch`，并在
`backend/config/yaml.go` 紧跟 `Memory` 之后新增类型：

```go
// WebSearch wires the local web_search function tool to a real search
// backend. Disabled by default to keep network egress opt-in.
type WebSearch struct {
	Enabled        bool   `yaml:"enabled"`
	Provider       string `yaml:"provider"`        // bocha | tavily | custom
	BaseURL        string `yaml:"base_url"`        // override per-provider default
	APIKey         string `yaml:"api_key"`
	APIKeyEnv      string `yaml:"api_key_env"`
	MaxResults     int    `yaml:"max_results"`     // default 5; LLM may override per call
	TimeoutSeconds int    `yaml:"timeout_seconds"` // default 30
}
```

`Config` 加一行：

```go
WebSearch WebSearch `yaml:"web_search"`
```

API key 解析复用 `normalizeModels` 里的同款 `api_key_env → api_key:$VAR
→ literal` 优先级 —— 如果嫌重复，提一个 `resolveAPIKey(envName,
literal string) string` 顶层函数共享给 models / web_search 两处用。
**矫枉过正预警 —— 当前 2 个 caller，先就地复制 6 行；第三个 caller 出现
时再抽函数**。

#### 2. 工具实现 (`backend/agent/tools/web_search.go` 新文件)

```go
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"eino-cli/backend/config"
)

type webSearchArgs struct {
	Query      string `json:"query"                 jsonschema:"required,description=Natural-language search query"`
	MaxResults int    `json:"max_results,omitempty" jsonschema:"description=Max results to return; defaults to provider config (commonly 5)"`
}

const webSearchToolDesc = `Search the live web for fresh information. Use when answering needs facts beyond the model's training cutoff (current weather, latest news, today's prices, recent releases). Returns titled snippets — cite or summarise them; do NOT pretend you searched when you did not.`

func GetWebSearchTool(cfg *config.Config) (tool.BaseTool, error) {
	wsCfg := cfg.WebSearch
	return utils.InferTool("web_search", webSearchToolDesc,
		func(ctx context.Context, in webSearchArgs) (string, error) {
			if !wsCfg.Enabled {
				return "", fmt.Errorf("web_search disabled in yaml/config.yaml")
			}
			max := in.MaxResults
			if max <= 0 {
				max = wsCfg.MaxResults
			}
			if max <= 0 {
				max = 5
			}
			return runBochaSearch(ctx, wsCfg, in.Query, max)
		})
}

// runBochaSearch hits Bocha's /v1/web-search and returns a markdown bullet
// list. Provider switching (tavily / custom) lives here as a small switch
// when more backends land — single function, no premature interface.
func runBochaSearch(ctx context.Context, cfg config.WebSearch, query string, max int) (string, error) {
	endpoint := cfg.BaseURL
	if endpoint == "" {
		endpoint = "https://api.bochaai.com/v1/web-search"
	}
	body, _ := json.Marshal(map[string]any{
		"query": query,
		"count": max,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	httpClient := &http.Client{Timeout: timeout}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var parsed struct {
		Data struct {
			WebPages struct {
				Value []struct {
					Name    string `json:"name"`
					URL     string `json:"url"`
					Snippet string `json:"snippet"`
				} `json:"value"`
			} `json:"webPages"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("web_search backend returned %d", resp.StatusCode)
	}
	results := parsed.Data.WebPages.Value
	if len(results) == 0 {
		return "No web results found for query.", nil
	}
	var buf bytes.Buffer
	for i, r := range results {
		fmt.Fprintf(&buf, "%d. [%s](%s)\n   %s\n", i+1, r.Name, r.URL, r.Snippet)
	}
	return buf.String(), nil
}
```

#### 3. 注册 (`backend/agent/tools/tools.go`)

```go
// === 已有 ===
func BuildBuiltinTools(root string) []tool.BaseTool {

// === 新 ===
func BuildBuiltinTools(cfg *config.Config) []tool.BaseTool {
	root := cfg.RootDir
	tools := []tool.BaseTool{
		mustBuild(GetAskClarificationTool()),
		mustBuild(GetLsTool(root)),
		// ... existing 14 entries unchanged ...
	}
	if cfg.WebSearch.Enabled {
		tools = append(tools, mustBuild(GetWebSearchTool(cfg)))
	}
	return tools
}
```

`enabled = false` 时不挂工具，避免 LLM 看到一个会立刻 error 的工具浪费
token —— 比 "工具永远在但 disabled 时返回错误" 干净。

#### 4. 同步调用点

```55:55:backend/agent/lead_agent.go
				Tools: tools.BuildBuiltinTools(cfg.RootDir),
```

改成 `tools.BuildBuiltinTools(cfg)`。

```418:421:backend/agent/tools/tools_test.go
func TestBuildBuiltinToolsCount(t *testing.T) {
	got := BuildBuiltinTools(t.TempDir())
	if len(got) != 15 {
```

改成 `BuildBuiltinTools(&config.Config{RootDir: t.TempDir()})`，count
仍是 15（默认 `WebSearch.Enabled=false`）。新增一个
`TestBuildBuiltinToolsWithWebSearch` 验证 enabled=true 时 count=16。

#### 5. 工具单元测试

`backend/agent/tools/web_search_test.go` 起一个 `httptest.Server` mock
博查响应，断言：

- 请求 path = `/v1/web-search`，header `Authorization: Bearer $key`，
  body `{"query": "...", "count": 5}`。
- 200 + 2 条结果 → 返回值含 markdown bullet。
- 404 → 返回 error。
- `enabled=false` → 工具调用立即 error，不发 HTTP。

### 取舍

- **设计选择**：单 `runBochaSearch` 函数 + 未来 `switch cfg.Provider`
  分流，**不**抽 `WebSearchBackend interface{ Search(...) }` —— 对应
  AGENTS.md「矫枉过正预警 —— 8+ 字段才考虑结构体」+「Behavior lives in
  plain top-level functions」。第二个后端落地时再 switch 即可，第三个
  再考虑接口。
- **设计选择**：`BuildBuiltinTools` 改签名为 `(cfg)` 而非新增
  `BuildBuiltinToolsWithWebSearch(cfg, root)` —— 对应「Pass less data /
  配置只用一个 config.Config」推论 3。当前 root 是从 cfg 衍生的，整个
  cfg 进去最自然。改动 2 个调用点即可。
- **副作用**：
  - `BuildBuiltinTools` 签名变更：`backend/agent/lead_agent.go:55` 1
    处生产调用 + `backend/agent/tools/tools_test.go:419` 1 处测试调用，
    一并改。
  - 默认 `web_search.enabled=false` → 现有用户不感知，工具集仍是 15。
  - prompt 里 `directToolExamples = "ls, read_file, web_search, etc."`
    （`backend/agent/prompt.go:64`）从「幻觉引用」变成「真实工具引用」
    —— 自带修复一个早就存在的 prompt 错觉。
- **风险**：
  - 博查 API 限流 / 鉴权失败 → 返回 error 给 LLM，LLM 一般会改写为
    「搜索失败」并继续推理，不 hard-fail 整个 turn。
  - 博查响应结构变更 → 解析层 (`parsed.Data.WebPages.Value`) 会拿到
    空数组 → 返回 "No web results found"，不 panic。
- **回滚**：
  - 软回滚：`yaml/config.yaml` 把 `web_search.enabled` 改回 false →
    工具不挂，行为退回现状。
  - 硬回滚：删除 `backend/agent/tools/web_search.go` +
    `backend/agent/tools/web_search_test.go`；从 `Config` / `yaml.go`
    删 `WebSearch` 字段；`tools.go` 回到 `BuildBuiltinTools(root)` 签
    名 + 删那个 if 分支；`lead_agent.go:55` 与 `tools_test.go:419` 调
    用点回滚。

---

## F2 · 方舟模型 yaml/config.yaml 配置示例

### 目标

让 user 的 Python 代码场景（方舟 + DeepSeek + 联网搜索）跑在仓库里，
yaml 配置层面 0 代码改动 —— 仓库已经支持 OpenAI 协议任意 base_url
（`backend/agent/models.go:50-56`）。

### 实现代码

把下列片段加到 user 的 `yaml/config.yaml`（**这个文件不入 git**）：

```yaml
default_model: ark-deepseek

models:
  - name: ark-deepseek
    provider: openai
    model: deepseek-v3-2-251201
    base_url: https://ark.cn-beijing.volces.com/api/v3
    api_key_env: ARK_API_KEY
    timeout_seconds: 60

web_search:
  enabled: true
  provider: bocha
  api_key_env: BOCHA_API_KEY
  max_results: 5
  timeout_seconds: 30
```

环境变量两个：

- `ARK_API_KEY=...`（方舟 API key）
- `BOCHA_API_KEY=...`（博查 API key）

启动后 lead agent 跑 chat/completions 走方舟、function tool 触发
`web_search` → 博查 → 结果回流 LLM，闭环。

### 取舍

- **设计选择**：复用现有 `provider: openai` 分支，**不**新增
  `provider: ark` —— 对应「Pass less data」+「行为住普通顶层函数 ——
  `models.go::buildChatModel` 的 switch 已经能处理任意 OpenAI 协议后端」。
  既然方舟兼容 OpenAI 协议，就让它走 openai 路径；只在文档 / 注释里
  提一句「方舟用 provider: openai + base_url」。
- **副作用**：仓库行为对原来用 Kimi 的用户 0 影响 —— 只要他们没改
  default_model。
- **风险**：方舟 OpenAI 兼容层若有非标准字段（比如
  `reasoning_effort` 不被识别），`models.go::parseReasoningEffort`
  返回的字符串会被原样发给方舟。最坏情况：方舟拒掉这个字段，
  ChatCompletion 报错。tradeoff 是接受这个风险——它在 yaml 里默认空，
  只有显式配 `reasoning_effort: low/medium/high` 才会触发。
- **回滚**：纯 yaml 改动；删掉对应 model entry / web_search 段即可。

---

## 实施顺序

```
F2（yaml 示例）   ─── 0 代码，可先验证模型连通性
F1.1（yaml shape）─┐
F1.2（工具实现）   ├── 同一个 commit；F1 整体一次性合入
F1.3（注册 + 调用点）┤
F1.4（测试）       ┘
```

**单 commit 粒度**：F1 整体 1 个 commit（`agent: add web_search tool with
yaml gating`）。F2 不占 commit（只是文档示例）。

---

## 验证

| Feature | 验证标准 |
|---|---|
| F1 工具注册 | `TestBuildBuiltinToolsCount` 仍绿（默认 15）；新增 `TestBuildBuiltinToolsWithWebSearch` 断言 enabled=true 时 16 |
| F1 schema | `web_search` 工具 description 出现在 LLM 工具列表（mock 一次 chat completion 验证 tools 字段含 `name: "web_search"`） |
| F1 后端契约 | `web_search_test.go` mock httptest.Server，断言请求 path / header / body / 响应解析 |
| F1 enabled=false | mock 服务不应该收到请求；工具调用直接返回 error |
| F2 模型连通 | `go run ./cmd/...` + 提一个简单问题，方舟返回 200 + 流式响应（手动验证，无自动 test） |

---

## 估算

| 项 | 行数 |
|---|---|
| `web_search.go` 新增 | ~120 行（含 import / struct / runBochaSearch / GetWebSearchTool） |
| `web_search_test.go` 新增 | ~80 行（4 个 case） |
| `yaml.go` 新增 `WebSearch` struct | ~10 行 |
| `types.go` `Config.WebSearch` 字段 | 1 行 |
| `tools.go` 改 `BuildBuiltinTools` 签名 + if 分支 | ~5 行 |
| `lead_agent.go` 调用点 | 1 行 |
| `tools_test.go` 调用点 + 新 test | ~30 行 |
| **合计** | **~250 行** |

实现时间预估：1 个工作日（含 Bocha API 实测 + 边界 case）。

---

## 落地状态

- 2026-05-16：F1 全量落地（types/yaml/web_search.go/tools.go/lead_agent.go
  + tools_test.go + web_search_test.go），`go build` / `go test` 全绿。
- 默认 disabled：现网无行为变化，开关由 `web_search.enabled: true` +
  `api_key_env: BOCHA_API_KEY` 启用。
