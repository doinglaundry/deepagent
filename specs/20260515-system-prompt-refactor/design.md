# 系统提示词重构

> **范围说明**：本 spec 的目标对象**不是 `eino-cli` 源码**，而是用户提供的一段
> 外部 LLM agent system prompt（含 SOUL / agent_discipline / subagent_system /
> clarification_system / memory / skill 注册表 / response_style 等多块）。本
> 文档按 `AGENTS.md` 约定的"三段式 + 软硬回滚"骨架落地，**不修改本仓库任何
> 运行时代码**。落盘到 `specs/` 仅作为外部 prompt 的设计参考；真正生效需要把
> 改动 patch 到 prompt 所在仓库。

## 总览

- **痛点**：上一轮诊断识别 4 处直接冲突（命名 / 工具签名 / mode / 语言）+ 2 处
  大量重复（memory dedup 缺失 / `<agent_discipline>` 逐字重复）。F2 在 ground
  后被大幅修订 —— 见下文，初稿误判工具空间。
- **改动范围**：5 个独立 feature（F1–F5），互相不依赖，可任意顺序合入；F1
  是 P0 必做（命名冲突），F2 surgical 修 1 行空白 bug，F3 收益最高（token），
  F4 是清扫，F5 合并到 F1 自动消化（0 行改动）。
- **不动 yaml shape**：本 spec 没有 yaml 配置改动。
- **预期收益（粗估）**：prompt 体积砍 ~10–15%（主要来自 F1 删 yaml/soul.md
  + F3 dedup），变量/函数命名歧义消除（F1）；F2 修复 `Available Subagents:`
  下空白行的 cosmetic bug。

引用约定：本仓库代码用 `起始行:结束行:路径`；外部 prompt 用 `<section_name>`
锚点（因为没有行号）。

---

## F1 · 单源代码风格

### 目标

外部 prompt `<soul>` 的 `### Code Style` 段与仓库根 `AGENTS.md` 直接冲突：

- `<soul>` Code Style 第 1 条：「变量要以动词开头，尽量使用 get、set。」
- 本仓库 `AGENTS.md` 「命名」章 —— 见

  ```30:35:AGENTS.md
  ## 命名
  - **变量名 = 语义本身**：`getID()` 的返回值叫 `id`，不叫 `result` /
   `r` / `tmp`。
  ```

  并明确：`**函数名以动词开头**`。

两条规则同时生效，agent 写代码时会出现 `getId := getID()` 这种语义错乱。

预期效果：

- 单一代码风格来源 = `AGENTS.md`。
- `<soul>` 只承载身份 / 沟通风格 / 成长 —— 跟代码风格无关的"人格层"。
- 删除后 agent 行为：写 `id := getID()`，不再出现 `getId` / `getResult`
  这种把变量当函数的命名。

### 实现代码

外部 prompt 的 `<soul>` 块：删除整个 `### Code Style` 段（4 条 bullet）。

```diff
 <soul>
   ### Identity
   ...
   ### Communication
   直接、简洁。默认语言：中文。无需切换语言。

-  ### Code Style
-  - 变量要以动词开头，尽量使用 get、set。
-  - 函数要尽可能简单。
-  - Error 不要放在 if 中进行判断，要单独一行进行判断。
-  - 减少功能的调用链，不要超过四层调用。

   ### Growth
   ...
 </soul>
```

`AGENTS.md` 不动 —— 它已经通过 `always_applied_workspace_rules` 自动注入。

### 取舍

- **设计选择**：单源 + 删除重复 —— 对应 `AGENTS.md` 「命名」章「重命名扫
  到底」原则（已落盘的 spec 文档也算调用点，重复定义视为多个调用点）。
- **副作用**：~80 token 减耗（4 行 bullet）；命名行为切回单一标准；不影响任何
  工具调用 / 中间件链。
- **风险**：低 —— 本仓库未发现任何依赖 SOUL 这 4 条的代码或文档。
- **回滚**：
  - 软回滚：不存在（无 flag）。
  - 硬回滚：把 4 行 bullet paste 回 `<soul>` 原位。

---

## F2 · subagent / clarification 段落保留 + 修空白 bug

### 目标

`backend/agent/prompt.go` 的 `<subagent_system>`（由 `buildSubagentSection`
生成）和 `<clarification_system>`（嵌在 `systemPromptTemplateRaw` 模板里）
是 agent 的 orchestration 与 clarification 行为引导，**保留**。

ground 后发现初稿诊断里的"工具签名错误"大半是误判：

- `task(subagent_type="general-purpose")`：eino-cli 真有这工具，签名
  `{subagent_type, description}`（`backend/agent/middlewares/subagent_limit.go:27`
  + `specs/20260514-feature-comparison/step3-subagent-task-tool.md:80-84`）；
  `"general-purpose"` 是 deep-agent 框架内置 subagent 的真实名字
  （`backend/skills/public/systematic-literature-review/SKILL.md:101` 实际
  使用过）。初稿误以为 `subagent_type` enum 是 Cursor IDE Task 工具的
  `generalPurpose | explore | ...`，那是另一个工具空间。
- `ask_clarification(question, clarification_type, context, options)`：
  eino-cli 真有这工具（`backend/agent/middlewares/clarification.go:17`
  `AskClarificationToolName = "ask_clarification"`），所有参数名跟
  `parseClarificationArgs:92-99` 解析结构**完全匹配**。初稿误以为唯一存在
  的实现是 Cursor IDE 的 `AskQuestion`，那是另一个工具空间。

唯一真 bug：`buildSubagentSection:61` `availableSubagents := ""`，使得
prompt 渲染出 `**Available Subagents:**` 之后是一行空白。

预期效果：

- `<subagent_system>` 与 `<clarification_system>` 整段保留。
- `**Available Subagents:**` 之后列出 deep-agent 框架内置的
  `general-purpose` subagent 的描述，prompt 不再有空白行。
- `IsSubagentEnabled=false` 时这两段仍然 collapse 不出现（行为不变）。

### 实现代码

```62:62:backend/agent/prompt.go
	availableSubagents := "- `general-purpose`: a fresh deep-agent instance with the same toolbelt; use it for context-isolated parallel research / extraction tasks."
```

`buildSubagentSection` 的剩余部分、`<clarification_system>` 整段、
`GetSubagentThinking / GetSubagentReminder / GetSubagentSection` 三个
helper、`GetSystemPrompt` 的 `IsSubagentEnabled` 参数、模板里的
`{subagent_thinking}` / `{subagent_section}` / `{subagent_reminder}` 三个
占位符 —— **均不动**。

### 取舍

- **设计选择**：保留两段；只 surgical 修 `availableSubagents` 一处空白。
  对应 AGENTS.md 「想清楚再下手 —— 不要假设、要 ground」。初稿的删除方案
  违反了「先 ground 再下手」—— 把 Cursor IDE 工具签名当成 eino-cli 工具
  签名。
- **副作用**：prompt 多 1 行（list item）；token 增量 < 30。
- **风险**：极低。`general-purpose` 是 deep-agent 框架硬编码 subagent 名，
  当 cfg 接入 named subagents（见 `step3-subagent-task-tool.md` 未来设计）
  时再扩展这个 list 也不会破坏现有行为。
- **回滚**：
  - 软回滚：不存在（无 flag）。
  - 硬回滚：把 `availableSubagents` 改回 `""` 一处即可。

---

## F3 · Memory 治理（dedup + 衰减 + 分层）

### 目标

外部 prompt 的 `<memory>` 块当前状态（直接观察）：

- 30+ 条 fact，confidence **全部 0.90**。
- 重复率 > 50%：「用户对 Git 感兴趣」出现 ≥3 次，「用户请求查看 git
  history」出现 ≥4 次，「用户用中文交流」出现 ≥3 次。
- 一次性 episodic 目标（"找 CHANGELOG.md 行数" / "soul.md 绝对路径"）
  当成长期偏好挂着，每次会话喂回。

后果：每会话浪费 ~600 token + 引导 agent 把一次性问题当作长期偏好。

预期效果：

- fact 总数 ≤10 条（dedup + 丢弃 episodic）。
- 区分 enduring / episodic，后者会话结束后自动驱逐。
- confidence 随频率 + 时间波动，不再永远 0.90。

### 实现代码

memory 写入端（不在 prompt 渲染层、在 memory pipeline 里）改三处：

**1. dedup**：写入前用 embedding 相似度合并到现有 fact —— 阈值
`cosine_sim ≥ 0.92` 视为同一 fact，confidence 取
`min(0.99, old + 0.05)`，不再追加新行。

```python
def upsert_fact(new_fact, store):
    new_emb = embed(new_fact.text)
    for old in store:
        if cosine(new_emb, old.embedding) >= 0.92:
            old.confidence = min(0.99, old.confidence + 0.05)
            old.last_seen = now()
            return
    store.append(new_fact)
```

**2. 分层标签**：每条 fact 加 `kind: enduring | episodic`，episodic 带
`expires_at`：

```yaml
- kind: enduring
  fact: "用户使用中文交流"
  confidence: 0.99
  last_seen: 2026-05-15T17:00:00+08:00

- kind: episodic
  fact: "用户想看 CHANGELOG.md 行数"
  confidence: 0.85
  expires_at: 2026-05-15T18:00:00+08:00  # 会话结束 + 1h
```

**3. 衰减 + 渲染过滤**：注入 prompt 的 `<memory>` block 时按下列规则
过滤：

```python
visible = [
    f for f in store
    if f.kind == "enduring"
       and f.confidence >= 0.5
       and (f.expires_at is None or f.expires_at > now())
]
visible.sort(key=lambda f: f.confidence, reverse=True)
return visible[:10]
```

每次写入新 enduring fact 时，对同主题的 episodic fact `confidence -= 0.1`，
低于 0.5 自动驱逐。

### 取舍

- **设计选择**：
  - **enduring vs episodic 分层** —— 对应 `AGENTS.md` 「核心原则」推论 1
    「结构体只装必须一起出现的状态」。会话级目标和长期偏好生命周期不同，
    不该混在一个 list 里。
  - **dedup 阈值 0.92** —— 经验值，先跑一周再调。
- **副作用**：
  - 当前 memory ~30 条（约 ~600 token）→ 清理后 ~10 条（约 ~200 token），
    单轮节省 ~400 token；多轮累积更显著。
  - dedup 写入端加一次 embedding 调用（~50ms / 写入），可接受。
- **风险**：
  - dedup 阈值过严 → "Git" 与 "git version control" 不合并；过松 → 不同
    主题被揉一起。先用 0.92 跑 7 天，看 fact 数曲线再调。
  - episodic `expires_at` 设短了会丢真实长期偏好 —— 默认"会话结束 + 1h"，
    enduring 标签由 promotion 规则升级（同 fact 出现 ≥3 个不同会话则
    promote 为 enduring）。
- **回滚**：
  - 软回滚：env `MEMORY_DEDUP=false` 关闭 dedup；env `MEMORY_KIND=disabled`
    所有 fact 当 enduring 处理 → 立即回到旧行为。
  - 硬回滚：revert 三处 pipeline 改动。

---

## F4 · 删除重复 `<agent_discipline>` 块

### 目标

外部 prompt 中 `<agent_discipline>` 完整内容出现 **2 次**：

- 一次嵌在 `AGENTS.md`（通过 `always_applied_workspace_rules` 自动注入）—— 见
  本仓库

  ```236:280:AGENTS.md
  ## Agent 工作纪律

  面向 LLM Agent 的执行守则。**这套规则向"谨慎"倾斜，不向"速度"倾斜；
  trivial 任务自行判断。**

  ### 1. 想清楚再下手
  ...
  ```

- 一次独立放在 user_query 上方 `<agent_discipline>` 标签 —— 内容**逐字
  相同**。

预期效果：

- 单一来源 = `AGENTS.md`。
- 修改时只改一处，不会出现"改了一处忘了另一处"的双源问题。

### 实现代码

prompt 拼装层（外层 user_query 渲染逻辑）删除独立的 `<agent_discipline>`
标签段。`AGENTS.md` 那份保留 —— 它由 workspace rule 自动注入，prompt 端
不需要管。

### 取舍

- **设计选择**：单源 —— 对应 `AGENTS.md` 「命名」章「重命名扫到底」（重复
  定义视为多个调用点）。
- **副作用**：~1100 token 减耗；行为不变（rule 内容来自同一份文本）。
- **风险**：无。两份逐字相同，删一份等价。
- **回滚**：软回滚不存在；硬回滚 = paste 回那个标签段。

---

## F5 · 语言策略统一

### 目标

外部 prompt 三处规则疑似冲突，需要逐条 ground 后定性：

- `<soul>` Communication：「默认语言：中文。无需切换语言。」
- `<critical_reminders>` 第 461 行：`Language Consistency: Keep using
  the same language as user's`
- `<response_style>`：整段用英文写成（行为风格层面，**非语言规则**）。

ground 结论：

- 三条里**只有 SOUL 那条真正冲突** —— 它强行定中文且禁止切换，跟
  `Language Consistency` 的"跟随用户语言"互斥。
- `<critical_reminders>` 的 `Language Consistency` **是正确规则**，保留。
- `<response_style>` 用英文写是**写作风格选择**（与 `AGENTS.md` 章节标题
  混合中英、prompt 模板代码注释为英文一致），不强制 agent 输出语言。
  保留英文不动。

预期效果：单一规则，明确 fallback —— 输出语言跟随用户当前消息；纯命令 /
代码无自然语言信号时由模型自行回落，不再有"默认中文且禁切换"的硬约束。

### 实现代码

**实质上不需要单独 commit** —— 冲突源（SOUL「无需切换语言」一行）将在
commit 5（F1：删除整个 `yaml/soul.md` 死文件）时一并消化。其它两处保留：

- `<response_style>`：**保持英文不动**（写作风格层面，与仓库其他 prompt
  代码风格一致）。
- `<critical_reminders>` 第 461 行 `Language Consistency: Keep using the
  same language as user's`：保留，这就是单一正确规则。

如果 commit 5 不做（用户决定保留 `yaml/soul.md`），再单独提一个 commit
改写 SOUL 那行：

```diff
- 直接、简洁。默认语言：中文。无需切换语言。
+ 直接、简洁。语言跟随用户当前消息。
```

### 取舍

- **设计选择**：「跟随用户消息语言」单一规则；写作风格（prompt 模板英文）
  与运行时输出语言（跟随用户）解耦 —— 它们不是同一件事。
- **副作用**：commit 5 落地后 SOUL 整条规则消失；`<response_style>` 与
  `<critical_reminders>` 不动 → 0 行改动。
- **风险**：无。
- **回滚**：跟随 commit 5 的回滚（恢复 `yaml/soul.md`）。

---

## 实施顺序与依赖

```
F1 ──┐
F2 ──┼── 互相独立，任意顺序合入
F3 ──┤
F4 ──┤
F5 ──┘  (合并到 F1：删 SOUL 一并消化；独立 0 行改动)
```

**单 commit 粒度**：F1–F4 各一个 commit；F5 不单独占 commit。

**优先级（建议）**：

1. **当天必做**：F1（命名冲突）—— P0，会直接产生错误行为。F5 跟着 F1
   自动完成。
2. **一周内**：F3（memory）—— 收益最大的 token 优化，越早做积累越少。
3. **顺手收掉**：F2（修空白 bug 1 行）+ F4（删重复 agent_discipline）。

---

## 验证

| Feature | 验证标准 |
|---------|---------|
| F1 | grep `<soul>` 不再出现 `Code Style`；agent 在 1 轮 Go 代码任务里使用 `id := getID()` 风格 |
| F2 | `GetSystemPrompt("default", true, cfg)` 输出里 `**Available Subagents:**` 之后立刻有 `general-purpose` 描述行（不空白）；其余 `<subagent_system>` / `<clarification_system>` 文本不变；`go test ./backend/agent/...` 全绿 |
| F3 | dedup 后 fact 数 ≤10；同义 fact（"Git" / "git version control"）合并；episodic fact 在下一会话不再出现 |
| F4 | grep `<agent_discipline>` 在 prompt 中只出现 1 次（嵌在 `AGENTS.md` 内） |
| F5 | 用英文提问时输出英文；用中文提问时输出中文；SOUL 那条「无需切换语言」消失 |

---

## 估算总收益

| 项 | token 减耗（粗估） |
|----|------|
| F1 删除 yaml/soul.md | ~600 |
| F2 修 `Available Subagents:` 空白（surgical，反而 +30 token） | -30 |
| F3 memory dedup（每轮节省） | ~400 |
| F4 删除重复 agent_discipline | ~1100 |
| F5 语言策略统一（合并到 F1，0 行改动） | 0 |
| **合计（单轮 prompt）** | **~2070 token** |

实际数字依 tokenizer 而定，量级稳定在「单 prompt 砍 ~10–15%」。F2 不再
追求 token 优化 —— 初稿删除方案被识别为对 eino-cli 工具空间的误判。

行为一致性：F1（命名）+ F5（语言）→ 跨边界 case 不再随机分裂；F2 修复
prompt 里仅有的 cosmetic 渲染 bug（空白行）。
