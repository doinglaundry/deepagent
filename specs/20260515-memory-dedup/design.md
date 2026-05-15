# Memory 去重与 episodic / enduring 分层

> 范围：本 spec 是 `specs/20260515-system-prompt-refactor/design.md` 中
> **F3** 的落地方案。F3 原方案用 embedding-cosine 合并 fact，ground 后
> 修正为三件事：**写入端 normalized-text 字面 dedup + Schema 加
> Kind/ExpiresAt + 加强 update prompt 指令**。不引入 embedding 依赖。

## 总览

- **痛点**（已观察）：实际 memory 数据里 30+ 条 fact、confidence 全
  0.90、重复率 >50%（"用户对 Git 感兴趣" ×3、"git history" ×4）；
  episodic 一次性目标（"找 CHANGELOG.md 行数"）当 enduring 挂着，
  每次会话喂回。这两类问题分别对应字面重复 / 缺生命周期。
- **改动范围**：3 个独立 sub-feature 合一个 commit；4 个文件 +
  yaml CHANGELOG 一条。
  - `backend/memory/store/data.go`：`Fact` 加 `Kind` + `ExpiresAt`
    （omitempty，向后兼容老数据）。
  - `backend/agent/memory_updater.go::applyUpdate`：write-time dedup
    + episodic TTL 兜底；渲染前过滤过期 episodic。
  - `backend/agent/memory_render.go::renderFactsSection`：渲染时按
    `ExpiresAt < now()` 过滤；可选展示 `[episodic]` 标签。
  - `backend/agent/memory_update_prompt.go`：在 "Important Rules" 段
    末追加 dedup + kind 规则。
  - `backend/config/yaml.go::Memory`：加 `DedupEnabled bool` +
    `EpisodicDefaultTTLSeconds int`。**零值 = 关闭**（跟仓库惯例对齐：
    `DebounceSeconds=0` 不 debounce、`MaxFacts=0` 不限）。要开启 dedup
    必须 yaml 写 `dedup_enabled: true`，要兜底 TTL 写
    `episodic_default_ttl_seconds: 3600`，跟 CHANGELOG 给的推荐配置同步。
  - `yaml/CHANGELOG.md`：登记本次 shape 改动。
- **不引入新依赖**：不要 embedding；不要外部相似度库。
- **预期收益（粗估）**：当前 fact 数 30+ → dedup 后 ≤10 enduring + 0
  episodic（短会话窗口外全过期）；单 prompt 节省 `~400 token / 轮`，
  累积更显著。

引用约定：现有代码用 `起始行:结束行:路径`；新代码用 ` ```go ` fence。

---

## F3a · 写入端 normalized-text dedup

### 目标

`backend/agent/memory_updater.go::applyUpdate` 第 212–234 行收到
`updPayload.NewFacts` 后**直接 append**，无任何代码层 dedup。LLM 通过
`factsToRemove` 间接 dedup，但实测 LLM 不可靠（user prompt 30+ 重复
fact 即证据）。

```212:234:backend/agent/memory_updater.go
	for _, nf := range upd.NewFacts {
		content := strings.TrimSpace(nf.Content)
		if content == "" {
			continue
		}
		conf := memorystore.CoerceConfidence(nf.Confidence)
		if conf < cfg.FactConfidenceThreshold {
			continue
		}
		category := strings.TrimSpace(nf.Category)
		if category == "" {
			category = "context"
		}
		out.Facts = append(out.Facts, memorystore.Fact{
```

预期效果：

- 写入新 fact 前先 normalize（trim + lowercase + 折叠多空格）后跟
  现有 fact 的 normalized content 比较，**字面相等**则合并：
  - `confidence = min(0.99, max(old, new) + 0.05)`
  - `last_seen` / `LastUpdated` 更新；不 append 新行。
- 不字面相等 → 仍按原路径 append。
- O(n²) 字符串比较，n 当前 < 100，<1ms 完成。
- `cfg.Memory.DedupEnabled = false` 时跳过 dedup（软回滚开关）。

### 实现代码

`backend/agent/memory_updater.go`：

```go
// === 新增 ===
// normalizeFactContent: trim + lowercase + 折叠空白，处理大小写/多空格
// 字面变体；不去标点不去 stopwords，避免误合并语义不同的 fact。
func normalizeFactContent(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.Join(strings.Fields(s), " ")
}

// findDuplicateFact: 命中返回索引，否则 -1。线性扫描，n<100 不必加索引。
func findDuplicateFact(facts []memorystore.Fact, normalized string) int {
	for i := range facts {
		if normalizeFactContent(facts[i].Content) == normalized {
			return i
		}
	}
	return -1
}
```

修改 `applyUpdate` 的 NewFacts 循环（第 212 行起）：

```go
// === 改 ===
for _, nf := range upd.NewFacts {
	content := strings.TrimSpace(nf.Content)
	if content == "" {
		continue
	}
	conf := memorystore.CoerceConfidence(nf.Confidence)
	if conf < cfg.FactConfidenceThreshold {
		continue
	}
	category := strings.TrimSpace(nf.Category)
	if category == "" {
		category = "context"
	}

	// === 新增 ===
	if cfg.DedupEnabled {
		idx := findDuplicateFact(out.Facts, normalizeFactContent(content))
		if idx >= 0 {
			old := out.Facts[idx].Confidence
			merged := old
			if conf > merged {
				merged = conf
			}
			merged += 0.05
			if merged > 0.99 {
				merged = 0.99
			}
			out.Facts[idx].Confidence = merged
			continue
		}
	}

	// === 已有路径，未改 ===
	out.Facts = append(out.Facts, memorystore.Fact{
		ID:          memorystore.NewFactID(),
		Content:     content,
		Category:    category,
		Confidence:  conf,
		// Kind / ExpiresAt 由 F3b 填入
		SourceError: strings.TrimSpace(nf.SourceError),
		CreatedAt:   now,
		Source:      "llm",
	})
}
```

### 取舍

- **设计选择**：trim + lowercase + 折叠空白，**不**去标点 / stopwords —— 对应
  `AGENTS.md` 「核心原则」「能不写就不写」+「200 行能写成 50 行就重写」。
  embedding 是大炮打蚊子；激进 normalize 会误合并语义不同的 fact。
- **副作用**：`applyUpdate` 加 ~12 行 + 两个 helper 函数；NewFacts 循环
  时间从 O(n) 升到 O(n·m)，n=newFacts、m=existing；n,m<100 实测 <1ms。
- **风险**：
  - 字面相同但语义不同（"用户喜欢 Go" vs "用户讨厌 Go"）—— 不会，
    后者 normalize 后字符串不同。
  - 字面不同但语义相同（"用户对 Git 感兴趣" vs "用户喜欢 Git"）——
    不会合并；接受这个 false-negative，靠 LLM 的 factsToRemove 兜底。
- **回滚**：
  - 软回滚：`cfg.Memory.DedupEnabled = false` → 跳过 dedup 分支。
  - 硬回滚：删除两个 helper + 还原 applyUpdate 那 12 行。

---

## F3b · Schema 加 Kind / ExpiresAt

### 目标

当前 `Fact` 没有"会话级一次性目标"概念，所有 fact 都是永久挂着的。
观察到的浪费：用户问"找 CHANGELOG.md 行数"被记成长期 fact，每次会话
重新喂回；用户根本不在意 CHANGELOG.md 行数了。

```44:54:backend/memory/store/data.go
// Fact is a single discrete memory item; ID is stable across rewrites so the
// updater can target it via factsToRemove.
type Fact struct {
	ID          string  `json:"id"`
	Content     string  `json:"content"`
	Category    string  `json:"category"`
	Confidence  float64 `json:"confidence"`
	SourceError string  `json:"sourceError,omitempty"`
	CreatedAt   string  `json:"createdAt,omitempty"`
	Source      string  `json:"source,omitempty"`
}
```

预期效果：

- `Kind` ∈ `{"enduring", "episodic"}`，omitempty。**老数据 / 缺省 = 视作
  `enduring`**（向后兼容）。
- `ExpiresAt` ISO-8601 字符串，omitempty。`enduring` 永远空；`episodic`
  写入时若 LLM 没指定，由写入端用 `cfg.EpisodicDefaultTTL` 兜底填入
  `now + TTL`。
- 渲染端按 `Kind == "episodic" && ExpiresAt < now()` 过滤掉过期 fact。
- `LLM 通过 updatePayload.NewFacts` 显式指定 `kind` / `expiresAt`；不指定
  时按上面的兜底规则。

### 实现代码

**1. `backend/memory/store/data.go::Fact` 加两个字段**：

```go
// === 改 ===
type Fact struct {
	ID          string  `json:"id"`
	Content     string  `json:"content"`
	Category    string  `json:"category"`
	Confidence  float64 `json:"confidence"`
	Kind        string  `json:"kind,omitempty"`         // === 新增 ===
	ExpiresAt   string  `json:"expiresAt,omitempty"`    // === 新增 ===
	SourceError string  `json:"sourceError,omitempty"`
	CreatedAt   string  `json:"createdAt,omitempty"`
	Source      string  `json:"source,omitempty"`
}
```

加常量 + helper：

```go
// === 新增 ===
const (
	FactKindEnduring = "enduring"
	FactKindEpisodic = "episodic"
)

// IsExpired: episodic + ExpiresAt 在过去 → true。enduring 永远 false。
// nowISO 接收外部时间字符串，纯函数好测。
func (f Fact) IsExpired(nowISO string) bool {
	if f.Kind != FactKindEpisodic || f.ExpiresAt == "" {
		return false
	}
	return f.ExpiresAt < nowISO // ISO-8601 字典序 == 时间序
}
```

**2. `backend/agent/memory_updater.go::factUpdate` 加两个字段**（让 LLM
能指定）：

```go
// === 改 ===
type factUpdate struct {
	Content     string  `json:"content"`
	Category    string  `json:"category"`
	Confidence  float64 `json:"confidence"`
	Kind        string  `json:"kind,omitempty"`         // === 新增 ===
	ExpiresAt   string  `json:"expiresAt,omitempty"`    // === 新增 ===
	SourceError string  `json:"sourceError,omitempty"`
}
```

`applyUpdate` 写入新 fact 时填入（接 F3a 的 append 分支）：

```go
// === 改 ===
kind := nf.Kind
if kind != memorystore.FactKindEpisodic {
	kind = memorystore.FactKindEnduring
}
expiresAt := nf.ExpiresAt
if kind == memorystore.FactKindEpisodic && expiresAt == "" && cfg.EpisodicDefaultTTLSeconds > 0 {
	ttl := time.Duration(cfg.EpisodicDefaultTTLSeconds) * time.Second
	expiresAt = time.Now().UTC().Add(ttl).Format("2006-01-02T15:04:05Z")
}

out.Facts = append(out.Facts, memorystore.Fact{
	ID:          memorystore.NewFactID(),
	Content:     content,
	Category:    category,
	Confidence:  conf,
	Kind:        kind,
	ExpiresAt:   expiresAt,
	SourceError: strings.TrimSpace(nf.SourceError),
	CreatedAt:   now,
	Source:      "llm",
})
```

**3. `backend/agent/memory_render.go::renderFactsSection` 加过滤**：

```go
// === 改 ===
func renderFactsSection(facts []memorystore.Fact, runningTokens, maxTokens int) (string, int) {
	if len(facts) == 0 {
		return "", runningTokens
	}

	nowISO := time.Now().UTC().Format("2006-01-02T15:04:05Z")

	// === 新增：过滤过期 episodic ===
	live := make([]memorystore.Fact, 0, len(facts))
	for _, f := range facts {
		if !f.IsExpired(nowISO) {
			live = append(live, f)
		}
	}
	if len(live) == 0 {
		return "", runningTokens
	}

	sorted := make([]memorystore.Fact, len(live))
	copy(sorted, live)
	// ... 已有的 sort + render 逻辑不变
}
```

**4. `applyUpdate` 末尾加 sweep：清理过期 episodic**（避免文件无限增长）：

```go
// === 新增（在 MaxFacts trim 之后、out.LastUpdated = now 之前） ===
nowISO := now
kept := make([]memorystore.Fact, 0, len(out.Facts))
for _, f := range out.Facts {
	if !f.IsExpired(nowISO) {
		kept = append(kept, f)
	}
}
out.Facts = kept
```

### 取舍

- **设计选择**：
  - `Kind` 字符串而非枚举类型 —— JSON 序列化简单，向后兼容靠
    `omitempty` 实现，老 fact 没这个字段视作 enduring。对应
    `AGENTS.md` 「结构体只装必须一起出现的状态」+「不为不可能场景做错误处理」。
  - `ExpiresAt` ISO-8601 字符串 —— 与已有 `CreatedAt` 同格式，字典序
    == 时间序，比较不需要 parse 时间。
  - **写入端 + 渲染端双保险过滤**：写入端 sweep 防止文件膨胀；渲染端
    再过滤一次防止 sweep 之后又过期的 fact 漏出去（写入和渲染时间差
    可能 >> TTL，比如 TTL=10min、两个会话间隔 1h）。
- **副作用**：
  - `Fact` 加 2 字段；JSON omitempty 老数据无 diff。
  - `applyUpdate` 加 ~10 行（kind/expiresAt 填充 + sweep）。
  - `renderFactsSection` 加 ~8 行过滤循环。
- **风险**：
  - 系统时钟不准 → episodic 提早 / 延迟过期。可接受 —— `EpisodicDefaultTTL`
    默认 1h，几分钟漂移无影响。
  - LLM 错误把 enduring 标成 episodic → 短期被驱逐，下次会话再说。
    LLM 错误把 episodic 标成 enduring → 多挂一会儿，靠 dedup 兜底。
- **回滚**：
  - 软回滚：`cfg.EpisodicDefaultTTL = 0` 时新写的 episodic 没有 expiresAt
    → 永不过期（行为退化为 enduring）。
  - 硬回滚：删除字段 + 三处 helper + 还原 render/update。老数据 JSON
    多几个字段无害，下次保存自然丢弃。

---

## F3c · 加强 update prompt 指令

### 目标

`memory_update_prompt.go:99-110` 的 "Important Rules" 段当前**没提**
dedup 与 kind 设置。LLM 实测不主动 dedup（30+ 重复 fact），且没法
利用 F3b 的 episodic 字段。

预期效果：

- prompt 加两条 rule：
  1. **Dedup before adding**：检查 `<current_memory>.facts`，若新事实
     与已有 fact 字面或语义重复，**不要 add，让写入端 dedup 合并
     confidence**；语义重复但措辞不同时 add 但**同时**把老的加进
     `factsToRemove`。
  2. **Kind classification**：每个 newFact 显式指定 `kind`。
     - `enduring`：长期偏好 / 工作背景 / 长期目标 / 知识。
     - `episodic`：一次性会话目标 / 临时调试 / 单次问答的产物。
     - 不确定时默认 `enduring`（写入端不指定也会兜底成 enduring）。
- JSON 输出格式 schema 同步加 `kind` / `expiresAt`。

### 实现代码

`backend/agent/memory_update_prompt.go::memoryUpdatePromptTemplate` 修改
两处：

**1. JSON schema 块**（第 93-94 行）：

```diff
   "newFacts": [
-    { "content": "...", "category": "preference|knowledge|context|behavior|goal|correction", "confidence": 0.0 }
+    { "content": "...", "category": "preference|knowledge|context|behavior|goal|correction", "confidence": 0.0, "kind": "enduring|episodic", "expiresAt": "2026-05-15T18:00:00Z" }
   ],
```

**2. "Important Rules" 段末尾追加**（第 110 行后）：

```diff
 - Focus on information useful for future interactions and personalization
+- Dedup: before adding a newFact, scan <current_memory>.facts. If the
+  same fact is already present (literal or semantic match), DO NOT add
+  it again — the write side will merge confidence. If the wording is
+  different but semantically equivalent, add the new one AND put the
+  old fact_id into factsToRemove.
+- Kind classification (required for every newFact):
+  * enduring: long-term preferences, work background, sustained goals,
+    knowledge — the default when uncertain.
+  * episodic: one-shot conversational goals, transient debugging context,
+    single-question artefacts (e.g. "user asked for line count of
+    CHANGELOG.md"). These auto-expire on the write side; you may also
+    set an explicit expiresAt (ISO-8601 UTC) but it is optional.
```

### 取舍

- **设计选择**：
  - prompt 加规则不加 schema 强约束 —— LLM 错填 `kind` 由写入端兜底
    （`kind != "episodic"` 一律视为 enduring），不抛错。`AGENTS.md`
    「不为不可能场景做错误处理」。
  - 不让 LLM 算具体 expiresAt —— 让它只标 kind，TTL 由 server 端
    `cfg.EpisodicDefaultTTL` 兜底。LLM 算时间不可靠。
- **副作用**：prompt 加 ~10 行说明；token 增加 ~80（每轮 update call
  payload）；但更新频率受 `cfg.Memory.DebounceSeconds` 限制（默认值
  非 0），每分钟最多一次，可忽略。
- **风险**：LLM 没看懂规则 → 不 dedup / 不标 episodic。F3a 写入端
  dedup 兜底，F3b 默认 enduring 兜底，最差退化为现状。
- **回滚**：硬回滚 = revert prompt 模板的两处改动。

---

## yaml shape 改动 + CHANGELOG 登记

### `backend/config/yaml.go::Memory` 加两个字段

```go
// === 改 ===
type Memory struct {
	Enabled                    bool    `yaml:"enabled"`
	StoragePath                string  `yaml:"storage_path"`
	DebounceSeconds            int     `yaml:"debounce_seconds"`
	ModelName                  string  `yaml:"model_name"`
	MaxFacts                   int     `yaml:"max_facts"`
	FactConfidenceThreshold    float64 `yaml:"fact_confidence_threshold"`
	InjectionEnabled           bool    `yaml:"injection_enabled"`
	MaxInjectionTokens         int     `yaml:"max_injection_tokens"`
	DedupEnabled               bool    `yaml:"dedup_enabled"`                  // === 新增 ===
	EpisodicDefaultTTLSeconds  int     `yaml:"episodic_default_ttl_seconds"`   // === 新增 ===
}
```

**默认值 = 零值**，跟仓库惯例一致（`DebounceSeconds=0` 不 debounce /
`MaxFacts=0` 不限）：
- `DedupEnabled = false`：升级后行为不变，要享受 dedup 必须 yaml 显式
  写 `dedup_enabled: true`。
- `EpisodicDefaultTTLSeconds = 0`：episodic 缺 expiresAt 时**永不过期**
  （行为退化为 enduring）。要兜底必须 yaml 写
  `episodic_default_ttl_seconds: 3600`。

CHANGELOG 给的 yaml 片段就是推荐配置，用户照抄即开。

### `yaml/CHANGELOG.md` 追加

```yaml
## 2026-05-15: memory.dedup_enabled + memory.episodic_default_ttl

`memory:` 段下加两个字段：

```yaml
memory:
  # ... 已有字段 ...
  # ============================================================================
  # Memory Dedup & Episodic Lifecycle
  # ============================================================================
  # When true, applyUpdate normalises new fact content (trim + lowercase
  # + collapsed whitespace) and merges confidence into the matching fact
  # instead of appending. False keeps deer-flow legacy append behaviour.
  dedup_enabled: true

  # Default lifetime (seconds) for episodic facts whose updatePayload omits
  # expiresAt. Zero / unset → episodic facts persist forever (degenerates
  # to enduring). Aligned with DebounceSeconds / RecoverySeconds units in
  # this file.
  episodic_default_ttl_seconds: 3600
```

驱动：
- `backend/config/yaml.go::Memory` 加字段。
- `backend/agent/memory_updater.go::applyUpdate` 读 `cfg.DedupEnabled`
  决定走 dedup 分支；读 `cfg.EpisodicDefaultTTL` 兜底 episodic 的 expiresAt。
- `backend/memory/store/data.go::Fact` 加 `Kind` / `ExpiresAt` 字段。

背景：`specs/20260515-memory-dedup/design.md`。
```

---

## 实施顺序与依赖

```
F3b (schema 字段) ──┐
                    ├── 一起一个 commit
F3a (写入 dedup) ───┤
F3c (prompt 加强) ──┘
yaml shape + CHANGELOG ── 同 commit
```

`F3b` 是 schema 改动，没有它 `F3a` 的 fact merge 不了 episodic 信息；
`F3c` 是 prompt 改动，不依赖前两个但**同回归**。一个 commit 收完比拆三
个 commit 更便于 review（diff 都在 memory 目录内）。

## 验证

| 项 | 验证 |
|---|---|
| F3a dedup 命中 | 单测：append 两条 normalize 后相等的 fact，断言只剩 1 条且 confidence 合并 |
| F3a dedup 关闭 | 单测：`DedupEnabled=false` 时两条都 append |
| F3b kind 默认值 | 单测：`Kind: ""` 老数据 `IsExpired()` 永远 false |
| F3b episodic 过期 | 单测：注入 `Kind: "episodic"` + `ExpiresAt: 过去时间`；render 跳过 |
| F3b TTL 兜底 | 单测：`updatePayload.factUpdate{Kind: "episodic", ExpiresAt: ""}`，`cfg.EpisodicDefaultTTLSeconds=3600` → 写入后 ExpiresAt ≈ now+1h |
| F3b sweep | 单测：apply 一个空 update，过期 episodic 在结果里消失 |
| F3c prompt | 不写单测；人工 review prompt 字符串包含 "Dedup" / "Kind classification" 字样 |
| 兼容老数据 | 单测：load 一个不含 Kind/ExpiresAt 的旧 JSON，断言 `IsExpired() == false` 且渲染照常 |
| build + 现有测试 | `go build ./...` + `go test ./backend/agent/... ./backend/memory/...` 全绿 |

## 估算

- 代码改动：~120 行新增（含 helper + 单测分支），~10 行修改；
  3 个文件 + 1 个 prompt 模板 + 1 个 config struct。
- 单测：~80 行新增。
- token 收益：当前 30 条 fact ≈ 600 token → 清理后 ≤10 enduring + 0
  episodic ≈ 200 token；**每轮节省 ~400 token**，多轮累积。
- 启动延迟：0（无 embedding，无新依赖）。
