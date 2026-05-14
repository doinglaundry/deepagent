# Cursor 文件 / 搜索 / 命令工具对齐方案

> 日期: 2026-05-14
> 状态: Proposed
> 范围: 只落技术方案,不实现代码。

---

## 0. 结论

对齐一组 Cursor 风格工具,覆盖文件与搜索、命令与运行两类能力:

- `read_file`
- `apply_patch`
- `delete_file`
- `glob`
- `rg`
- `semantic_search`
- `read_lints`
- `shell`
- `await_shell`

所有新增或更新的工具都通过 `utils.InferTool` 构建。工具函数只接收参数结构体,返回字符串。行为放在顶层函数里,结构体只承载 tool 入参,不引入工具对象方法或依赖注入容器。

已有 `ls/read_file/write_file/edit_file/glob/grep/execute/ask_clarification` 必须保留,不能直接删除。和 Cursor 工具重复的能力优先升级现有工具语义;只有现有工具名或职责无法承载时才新增工具名。

---

## 1. 目标

- **G1** 文件与搜索工具的行为尽量贴近 Cursor Agent: 已知路径直接读,文件名 glob 默认递归,内容搜索走 ripgrep 语义。
- **G2** 命令工具支持短命令同步返回,长命令后台运行,并能通过 `await_shell` 继续观察。
- **G3** 每个工具实现都使用 `utils.InferTool`,保持本仓库当前工具注册风格。
- **G4** 所有路径默认限制在 `cfg.RootDir` 内;绝对路径允许读取 / 执行前先做安全校验。
- **G5** 工具输出直接给模型复用,优先输出绝对路径,避免模型自己拼错路径。

## 2. 非目标

- **NG1** 不实现 Cursor 私有后端能力。`semantic_search` 和 `read_lints` 只能落本仓库可本地实现的版本。
- **NG2** 不新增配置子结构。需要开关或限制时读取现有 `config.Config`,或先使用硬编码默认值。
- **NG3** 不把行为藏进带 receiver 的工具对象。工具执行逻辑放顶层函数。
- **NG4** 不做跨会话持久化 shell job。后台任务只在当前 CLI 进程内有效。
- **NG5** 不修改 `yaml/config.yaml` shape,所以本方案本身不需要改 `yaml/CHANGELOG.md`。

---

## 3. 统一约定

### 3.1 文件位置

新增 / 复用文件:

```text
backend/agent/tools/apply_patch.go
backend/agent/tools/delete_file.go
backend/agent/tools/rg.go
backend/agent/tools/semantic_search.go
backend/agent/tools/read_lints.go
backend/agent/tools/shell.go
backend/agent/tools/await_shell.go
backend/agent/tools/shell_jobs.go
```

`read_file` / `glob` 已存在,优先在原文件内升级语义。方案层面要求函数边界清晰,不要把 9 个工具塞进一个大文件。

### 3.2 注册入口

`BuildBuiltinTools(root string)` 继续作为唯一注册入口:

```go
func BuildBuiltinTools(root string) []tool.BaseTool {
    return []tool.BaseTool{
        mustBuild(GetAskClarificationTool()),
        mustBuild(GetLsTool(root)),
        mustBuild(GetReadFileTool(root)),
        mustBuild(GetWriteFileTool(root)),
        mustBuild(GetEditFileTool(root)),
        mustBuild(GetGlobTool(root)),
        mustBuild(GetGrepTool(root)),
        mustBuild(GetExecuteTool(root)),
        mustBuild(GetApplyPatchTool(root)),
        mustBuild(GetDeleteFileTool(root)),
        mustBuild(GetRgTool(root)),
        mustBuild(GetSemanticSearchTool(root)),
        mustBuild(GetReadLintsTool(root)),
        mustBuild(GetShellTool(root)),
        mustBuild(GetAwaitShellTool(root)),
    }
}
```

现有工具保留,新增工具排在旧工具之后:

- `read_file` / `glob` 直接升级现有实现,不新增同名并行文件。
- `grep` 保留;新增 `rg` 承载 Cursor ripgrep 参数模型。
- `execute` 保留;新增 `shell` / `await_shell` 承载后台任务模型。
- `write_file` / `edit_file` 保留;新增 `apply_patch` 承载 Cursor patch 协议。
- `ask_clarification` 保持现状,不参与 Cursor 工具对齐。

### 3.3 现有工具处理原则

| 现有工具 | 处理 | 原因 |
|---|---|---|
| `ls` | 保留 | Cursor 文件工具没有完全等价项;目录快速查看仍有价值 |
| `read_file` | 更新 | 名称和 Cursor 一致,可以直接补齐 offset / limit / 绝对路径语义 |
| `write_file` | 保留 | 全文件写入和 patch 修改是两种编辑模式;不能用 `apply_patch` 直接替代 |
| `edit_file` | 保留 | 现有精确替换能力仍可用;后续可在 prompt 中弱化推荐 |
| `glob` | 更新 | 名称和 Cursor 一致,直接升级默认递归和绝对路径输出 |
| `grep` | 保留 | 旧调用方可能依赖;新增 `rg` 提供更完整 Cursor 参数 |
| `execute` | 保留 | 旧同步命令入口继续可用;新增 `shell` / `await_shell` 处理长任务 |
| `ask_clarification` | 保留 | 本仓库 agent 控制流工具,不属于 Cursor 文件 / 命令能力 |

判断标准:

- 同名且职责一致: 更新现有工具。
- 职责相近但参数模型明显不同: 保留旧工具,新增 Cursor 名称。
- 旧工具已有稳定行为: 不删除,不改名。
- prompt 可推荐新工具,但注册表继续保留旧工具。

### 3.4 路径安全

所有文件工具共用顶层函数:

```go
func getResolvedPath(root, path string) (string, error)
func getRelativePath(root, path string) string
func isInsideRoot(root, path string) bool
```

规则:

- 空 path 返回错误。
- 相对路径基于 `root`。
- 绝对路径先 `filepath.Clean`,再判断是否位于 `root` 内。
- 需要允许读 workspace 外文件时,必须单独列白名单;本方案默认不允许。
- 输出路径统一用绝对路径,除非工具协议明确要求相对路径。

### 3.5 输出截断

统一顶层函数:

```go
func truncateToolOutput(s string, maxBytes int) string
```

默认值:

- 单次工具输出最多 64 KiB。
- 超限时保留前 64 KiB,追加 `\n[output truncated: N bytes omitted]`。
- `read_file` 按行 limit 优先,不靠字节截断兜底。

### 3.6 错误输出

工具函数返回 `("", err)` 只用于系统级失败,例如权限拒绝、命令无法启动、参数非法。

业务级失败返回字符串:

- `No files found`
- `No matches found`
- `Command failed with exit code N`
- `No diagnostics`

这样模型能继续推理,不会因为普通查找失败中断整轮。

---

## 4. 文件与搜索工具

## 4.1 `read_file`

### 入参

```go
type readFileArgs struct {
    FilePath string `json:"file_path" jsonschema:"required,description=Absolute or workspace-relative path to read"`
    Offset   int    `json:"offset" jsonschema:"description=1-based line offset; omit or <=0 starts at line 1"`
    Limit    int    `json:"limit" jsonschema:"description=Maximum number of lines; omit or <=0 uses default"`
}
```

### InferTool

```go
func GetReadFileTool(root string) (tool.BaseTool, error) {
    return utils.InferTool("read_file", readFileDesc,
        func(ctx context.Context, in readFileArgs) (string, error) {
            return readFile(ctx, root, in)
        })
}
```

### 实现细节

`readFile` 顶层函数执行:

1. `path := getResolvedPath(root, in.FilePath)`。
2. `os.Stat(path)`,目录返回错误: `path is a directory`。
3. 根据扩展名分流:
   - 图片: 返回 `image file: <abs path> (<bytes> bytes)`,暂不做 OCR。
   - PDF: v1 返回不支持说明;后续可接本地 PDF 文本提取。
   - 普通文本: `os.ReadFile`。
4. `strings.Split(string(data), "\n")`。
5. `offset <= 0` 时设为 1。
6. `limit <= 0` 时设为 2000。
7. 输出格式沿用现有实现: `     1\tline`。

### 测试

- 读相对路径。
- 读绝对路径。
- offset/limit。
- 目录报错。
- 不存在文件报错。
- 路径逃逸被拒绝。

## 4.2 `apply_patch`

### 入参

```go
type applyPatchArgs struct {
    Patch string `json:"patch" jsonschema:"required,description=Patch text in the repository patch format"`
}
```

### InferTool

```go
func GetApplyPatchTool(root string) (tool.BaseTool, error) {
    return utils.InferTool("apply_patch", applyPatchDesc,
        func(ctx context.Context, in applyPatchArgs) (string, error) {
            return applyPatch(ctx, root, in.Patch)
        })
}
```

### 实现细节

`apply_patch` 不直接 shell 调 `apply_patch`。实现一个小解析器,只支持本仓库需要的文件级 patch 子集:

- `*** Begin Patch`
- `*** Add File: <path>`
- `*** Update File: <path>`
- `@@`
- 以 `+` / `-` / space 开头的 hunk 行
- `*** End Patch`

执行流程:

1. `parsePatch(in.Patch)` 得到 `[]patchFileOp`。
2. 每个 op 的路径都过 `getResolvedPath`。
3. `Add File` 要求目标不存在;父目录存在或由工具创建。
4. `Update File` 先读当前文件,按 hunk 顺序做字符串匹配。
5. 每个 hunk 的 context 必须唯一命中,否则返回错误。
6. 所有 op 先 dry-run 成功,再写文件。
7. 写文件使用 `os.WriteFile`,权限沿用旧文件;新文件用 `0644`。

不支持:

- binary patch。
- rename。
- chmod。
- 多文件 op 部分成功。

### 测试

- 新增文件。
- 单 hunk 更新。
- 多 hunk 更新。
- context 不匹配报错。
- context 多处匹配报错。
- 目标路径逃逸被拒绝。
- dry-run 失败时不写任何文件。

## 4.3 `delete_file`

### 入参

```go
type deleteFileArgs struct {
    FilePath string `json:"file_path" jsonschema:"required,description=Absolute or workspace-relative file path to delete"`
}
```

### InferTool

```go
func GetDeleteFileTool(root string) (tool.BaseTool, error) {
    return utils.InferTool("delete_file", deleteFileDesc,
        func(ctx context.Context, in deleteFileArgs) (string, error) {
            return deleteFile(ctx, root, in.FilePath)
        })
}
```

### 实现细节

1. `path := getResolvedPath(root, in.FilePath)`。
2. `os.Lstat(path)`。
3. 文件不存在返回 `File does not exist: <abs path>`。
4. 目录拒绝删除: `refusing to delete directory`。
5. symlink 当文件处理,删除 symlink 自身。
6. `os.Remove(path)`。
7. 返回 `Deleted file <abs path>`。

### 测试

- 删除普通文件。
- 文件不存在。
- 目录拒绝。
- symlink 删除自身。
- 路径逃逸拒绝。

## 4.4 `glob`

### 入参

```go
type globArgs struct {
    Pattern string `json:"pattern" jsonschema:"required,description=Glob pattern; bare patterns search recursively"`
    Path    string `json:"path" jsonschema:"description=Directory to search in; omit to use workspace root"`
}
```

### InferTool

```go
func GetGlobTool(root string) (tool.BaseTool, error) {
    return utils.InferTool("glob", globDesc,
        func(ctx context.Context, in globArgs) (string, error) {
            return globFiles(ctx, root, in)
        })
}
```

### 实现细节

1. `searchBase := root`;如果 `Path` 非空,用 `getResolvedPath`。
2. `pattern := normalizeGlobPattern(in.Pattern)`。
3. `normalizeGlobPattern` 规则:
   - 空 pattern 报错。
   - 已以 `**/` 开头: 原样。
   - 否则: 自动补 `**/`。
4. 使用 `doublestar.FilepathGlob(filepath.Join(searchBase, pattern))`。
5. 过滤目录,只返回文件。
6. `filepath.Abs` 后排序。
7. 无结果返回 `No files found`。
8. 返回绝对路径,一行一个。

### 测试

- `CHANGELOG.md` 找到 `yaml/CHANGELOG.md`。
- `*.go` 能递归找子目录 Go 文件。
- 显式 `**/*.md` 不重复补前缀。
- `Path` 限定子目录。
- 无结果。

## 4.5 `rg`

### 入参

```go
type rgArgs struct {
    Pattern    string `json:"pattern" jsonschema:"required,description=Regular expression to search for"`
    Path       string `json:"path" jsonschema:"description=File or directory to search; omit for workspace root"`
    Glob       string `json:"glob" jsonschema:"description=Optional glob filter"`
    OutputMode string `json:"output_mode" jsonschema:"description=content, files_with_matches, or count"`
    Before     int    `json:"before" jsonschema:"description=Lines before each match"`
    After      int    `json:"after" jsonschema:"description=Lines after each match"`
    Context    int    `json:"context" jsonschema:"description=Lines before and after each match"`
    IgnoreCase bool   `json:"ignore_case" jsonschema:"description=Case-insensitive search"`
    HeadLimit  int    `json:"head_limit" jsonschema:"description=Maximum matches or files to return"`
}
```

### InferTool

```go
func GetRgTool(root string) (tool.BaseTool, error) {
    return utils.InferTool("rg", rgDesc,
        func(ctx context.Context, in rgArgs) (string, error) {
            return runRipgrep(ctx, root, in)
        })
}
```

### 实现细节

使用外部 `rg` 命令,不自己实现正则搜索。

命令构造:

- 基础: `rg --line-number --color never`。
- `OutputMode=files_with_matches`: 加 `--files-with-matches`。
- `OutputMode=count`: 加 `--count-matches`。
- `OutputMode=content` 或空: 默认内容输出。
- `Before/After/Context`: 映射到 `-B/-A/-C`。
- `IgnoreCase`: `-i`。
- `Glob`: `--glob <glob>`。
- `HeadLimit`: 输出后由 Go 侧截断,不依赖 shell pipe。

执行:

1. 参数用 `exec.CommandContext`,不拼 shell 字符串。
2. `cmd.Dir = root`。
3. 搜索路径过 `getResolvedPath` 后传给 `rg`。
4. `rg exit 1` 表示无匹配,返回 `No matches found`。
5. 其他非 0 返回错误。
6. 输出路径尽量保持 `rg` 原格式;如果传入绝对 `Path`,返回可能是绝对路径。

### 测试

- content 输出。
- files_with_matches。
- count。
- 无匹配返回 `No matches found`。
- glob filter。
- ignore_case。
- 正则非法返回错误。

## 4.6 `semantic_search`

### 入参

```go
type semanticSearchArgs struct {
    Query string `json:"query" jsonschema:"required,description=Natural-language question about code"`
    Path  string `json:"path" jsonschema:"description=Optional directory or file to search"`
}
```

### InferTool

```go
func GetSemanticSearchTool(root string) (tool.BaseTool, error) {
    return utils.InferTool("semantic_search", semanticSearchDesc,
        func(ctx context.Context, in semanticSearchArgs) (string, error) {
            return semanticSearch(ctx, root, in)
        })
}
```

### 实现细节

本仓库没有 Cursor 的语义索引,所以 v1 做本地可落地的近似实现:

1. `queryTerms := getSemanticTerms(in.Query)`。
2. 调 `rg` 搜这些词的 OR 正则。
3. 读取匹配文件周边片段。
4. 用简单打分排序:
   - 文件路径命中 query term +3。
   - 符号名命中 +2。
   - 注释 / 字符串命中 +1。
   - 同文件多词命中加权。
5. 返回最多 10 个 chunk:
   - 绝对路径。
   - 行号范围。
   - 片段内容。
   - 命中的关键词。

后续可替换为真正 embedding 索引,但工具签名不变。

### 测试

- 查询 `tool call` 能找到 middleware/tool 相关文件。
- path 限定生效。
- 无结果返回 `No semantic matches found`。
- 大文件只返回片段,不整文件输出。

## 4.7 `read_lints`

### 入参

```go
type readLintsArgs struct {
    Paths []string `json:"paths" jsonschema:"description=Optional files or directories to lint"`
}
```

### InferTool

```go
func GetReadLintsTool(root string) (tool.BaseTool, error) {
    return utils.InferTool("read_lints", readLintsDesc,
        func(ctx context.Context, in readLintsArgs) (string, error) {
            return readLints(ctx, root, in.Paths)
        })
}
```

### 实现细节

Cursor 的 `ReadLints` 读取 IDE diagnostics。本仓库本地 CLI 没有 IDE diagnostics API,所以 v1 使用可执行检查近似:

- Go 文件: `go test` 对应 package,或 `go test ./...`。
- Markdown: 不做 lint,返回 `No diagnostics provider for markdown`。
- 其他文件: 返回 `No diagnostics provider`。

Go 路径映射:

1. path 为空: `go test ./...`。
2. path 是 `.go` 文件: 找 package dir,执行 `go test <dir>`。
3. path 是目录: 执行 `go test <dir>/...`。
4. 输出通过 `truncateToolOutput`。
5. 成功返回 `No diagnostics`。
6. 失败返回测试输出,不作为 tool error。

### 测试

- 空 path 时命令构造为 `go test ./...`。
- 文件 path 映射到 package。
- 成功输出 `No diagnostics`。
- 失败输出包含失败内容。

---

## 5. 命令与运行工具

## 5.1 `shell`

### 入参

```go
type shellArgs struct {
    Command      string `json:"command" jsonschema:"required,description=Shell command to run"`
    WorkingDir   string `json:"working_directory" jsonschema:"description=Working directory; omit for workspace root"`
    TimeoutMS    int    `json:"timeout_ms" jsonschema:"description=Foreground wait timeout in milliseconds"`
    Description  string `json:"description" jsonschema:"description=Short human-readable command description"`
}
```

### InferTool

```go
func GetShellTool(root string) (tool.BaseTool, error) {
    return utils.InferTool("shell", shellDesc,
        func(ctx context.Context, in shellArgs) (string, error) {
            return runShell(ctx, root, in)
        })
}
```

### 实现细节

1. `Command` 不能为空。
2. `WorkingDir` 过 `getResolvedPath`;必须是目录。
3. 默认 `TimeoutMS = 30000`。
4. 用 `exec.CommandContext(ctx, "bash", "-lc", in.Command)`。
5. stdout/stderr 合并输出。
6. 如果命令在 timeout 内结束:
   - exit 0: 返回输出;空输出返回 `[Command executed successfully with no output]`。
   - exit 非 0: 返回输出 + `[Command failed with exit code N]`。
7. 如果超过 timeout:
   - 不杀进程。
   - 将进程登记到内存 job registry。
   - 返回 `Command is still running in background. task_id=<id>`。

后台 job registry:

```go
type shellJob struct {
    ID          string
    Command     string
    WorkingDir  string
    StartedAt   time.Time
    Done        bool
    ExitCode    int
    Output      strings.Builder
}
```

行为放顶层函数:

```go
func startShellJob(ctx context.Context, root string, in shellArgs) (*shellJob, error)
func getShellJob(id string) (*shellJob, bool)
func appendShellOutput(job *shellJob, chunk []byte)
func finishShellJob(job *shellJob, exitCode int)
```

`shellJob` 只是状态容器,不挂方法。

### 安全规则

`shell` 不做复杂命令解析。只做硬拦截:

- 空命令拒绝。
- `WorkingDir` 逃逸拒绝。
- 超长输出截断。
- 进程数上限,默认 8 个后台 job。

### 测试

- 成功命令。
- 非 0 exit。
- 空输出。
- timeout 后返回 task_id。
- working_directory 生效。
- working_directory 逃逸拒绝。

## 5.2 `await_shell`

### 入参

```go
type awaitShellArgs struct {
    TaskID       string `json:"task_id" jsonschema:"required,description=Background shell task id"`
    TimeoutMS    int    `json:"timeout_ms" jsonschema:"description=Maximum wait time"`
    Pattern      string `json:"pattern" jsonschema:"description=Optional regex to wait for in output"`
    SinceOffset  int    `json:"since_offset" jsonschema:"description=Return output starting at byte offset"`
}
```

### InferTool

```go
func GetAwaitShellTool(root string) (tool.BaseTool, error) {
    return utils.InferTool("await_shell", awaitShellDesc,
        func(ctx context.Context, in awaitShellArgs) (string, error) {
            return awaitShell(ctx, in)
        })
}
```

### 实现细节

1. 查 job registry。
2. 不存在返回错误: `unknown task_id`。
3. 默认 `TimeoutMS = 30000`。
4. 如果 `Pattern` 非空:
   - 编译 regexp。
   - 循环等到输出匹配、job 完成或 timeout。
5. 如果 `Pattern` 为空:
   - 等到 job 完成或 timeout。
6. 返回:
   - task id。
   - running/done。
   - exit code,如果已结束。
   - output offset。
   - output 内容。
7. job 完成后保留最近 32 个 job;超过上限按完成时间淘汰。

### 测试

- 等待完成。
- 等待 pattern 命中。
- timeout 返回 running。
- unknown task id。
- since_offset 只返回增量。

---

## 6. 工具描述约定

每个 tool desc 要写给模型,不是写给人类 API 用户。

示例:

```go
const globDesc = `Find files by glob pattern. Bare patterns are recursive: "CHANGELOG.md" searches as "**/CHANGELOG.md". Returns absolute file paths, one per line. Use this when you know a filename or path pattern.`
```

描述必须包含:

- 何时使用。
- 输入语义。
- 输出格式。
- 关键限制。

不要写长段实现解释。实现细节在 spec 和测试里。

---

## 7. 测试计划

### 单元测试

```text
go test ./backend/agent/tools
```

覆盖:

- 每个 tool 的 `Info()` name / schema。
- 每个 tool 的成功路径。
- 每个 tool 的失败路径。
- 路径逃逸。
- 输出截断。

### 集成测试

新增 `backend/runtime/eino/tool_parity_test.go` 可选:

- mock model 先调用 `glob("CHANGELOG.md")`。
- 再调用 `read_file` 读取 glob 返回路径。
- 最终回答行数。

### 手工验证

在 TUI 中输入:

```text
查找 CHANGELOG.md 的绝对路径,然后读取并统计行数
```

期望:

- `glob` 返回 `/Users/.../LLM/yaml/CHANGELOG.md`。
- `read_file` 使用该绝对路径。
- 最终回答只包含行数,工具 trace 灰色展示。

---

## 8. 分阶段落地

### Phase 1: 文件工具

实现:

- `read_file`
- `apply_patch`
- `delete_file`
- `glob`
- `rg`

验证:

```text
go test ./backend/agent/tools
```

### Phase 2: 命令工具

实现:

- `shell`
- `await_shell`
- `shell_jobs.go`

验证:

```text
go test ./backend/agent/tools
```

### Phase 3: 本地近似能力

实现:

- `semantic_search`
- `read_lints`

验证:

```text
go test ./backend/agent/tools
```

### Phase 4: prompt 和工具推荐顺序

检查:

- prompt 中推荐工具名是否和新工具一致。
- 旧 `ls/read_file/write_file/edit_file/glob/grep/execute/ask_clarification` 是否仍在注册表里。
- prompt 是否优先推荐 `rg` / `shell` / `apply_patch`,但不要求删除旧工具。
- TUI tool block 是否按新工具名正常展示。

---

## 9. 风险

### `semantic_search` 名称可能过度承诺

v1 只是本地近似语义搜索,不是 Cursor 索引。工具描述必须说清: `semantic_search` is a local heuristic search in this CLI。

### `shell` 后台进程泄漏

必须限制后台 job 数量,并在 CLI 退出时 kill 仍在运行的子进程。否则长时间 server / watcher 会残留。

### `apply_patch` 解析器复杂度

只支持本仓库需要的 patch 子集。不要追求兼容 git diff 全量语法。

### 工具太多导致模型混乱

实现后需要调整 prompt 和工具注册顺序,但不能删除旧工具。推荐路径:

- 文件名查找: 优先 `glob`,保留 `ls`
- 内容查找: 优先 `rg`,保留 `grep`
- 已知文件读取: `read_file`
- 小范围修改: 优先 `apply_patch`,保留 `edit_file` / `write_file`
- 命令: 优先 `shell` / `await_shell`,保留 `execute`

---

## 10. 验收标准

- `BuildBuiltinTools` 保留现有 `ls/read_file/write_file/edit_file/glob/grep/execute/ask_clarification`。
- `BuildBuiltinTools` 额外注册需要新增的 Cursor 对齐工具。
- 所有工具由 `utils.InferTool` 构建。
- `glob("CHANGELOG.md")` 返回嵌套文件的绝对路径。
- `read_file` 能直接读取 `glob` 返回路径。
- `rg` 无匹配返回 `No matches found`,不让 agent 中断。
- `shell` 超时返回 task id。
- `await_shell` 能读取后台任务输出。
- `apply_patch` dry-run 失败不写文件。
- `go test ./backend/agent/tools` 通过。
