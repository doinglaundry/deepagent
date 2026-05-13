# 🦌 SGADK CLI

中文

[![Go](https://img.shields.io/badge/Go-1.24%2B-00ADD8?logo=go&logoColor=white)](./go.mod)
[![Eino](https://img.shields.io/badge/Eino-ADK-5C7CFA)](https://github.com/cloudwego/eino)
[![Bubble Tea](https://img.shields.io/badge/TUI-Bubble%20Tea-FF69B4)](https://github.com/charmbracelet/bubbletea)

SGADK CLI 是一个面向本地开发的终端智能体客户端。它基于 **Eino ADK** 组装 Deep Agent runtime，用 **Bubble Tea** 提供 TUI，对接本仓库的工具、memory、skills、长期人格文件 `soul.md` 和可重载 agent 服务。

它的目标不是做一个通用聊天壳，而是给这个仓库提供一个稳定的 agent 工作台：启动快、配置本地化、工具输出可见、长期偏好可持久化，必要时可以在 TUI 里直接 `/reload` 重启 agent。

---

## 目录

- [🦌 SGADK CLI](#-sgadk-cli)
  - [一句话交给 Coding Agent 安装](#一句话交给-coding-agent-安装)
  - [快速开始](#快速开始)
    - [配置](#配置)
    - [运行 CLI](#运行-cli)
    - [安装全局命令](#安装全局命令)
  - [核心特性](#核心特性)
    - [TUI Agent 工作台](#tui-agent-工作台)
    - [工具输出折叠](#工具输出折叠)
    - [SOUL 长期人格](#soul-长期人格)
    - [Memory 与 Checkpoint](#memory-与-checkpoint)
    - [运行时 Reload](#运行时-reload)
  - [TUI 命令](#tui-命令)
  - [项目结构](#项目结构)
  - [开发](#开发)
  - [安全与本地文件](#安全与本地文件)

## 一句话交给 Coding Agent 安装

如果你在用 Cursor、Claude Code、Codex、Windsurf 或其他 coding agent，可以直接把下面这句话发给它：

```text
帮我在 /Users/bytedance/go/src/content/LLM 里检查配置，然后运行 scripts/install-sgadk.sh 安装 sgadk 命令；如果缺少 yaml/config.yaml 或 PATH 没配好，请告诉我下一步要补什么。
```

这条提示词是给 coding agent 用的。它会进入仓库，检查本地配置，安装 `sgadk` wrapper，并在结束时告诉你下一条启动命令。

## 快速开始

### 配置

1. **进入仓库**

   ```bash
   cd /Users/bytedance/go/src/content/LLM
   ```

2. **准备本地配置**

   主配置文件是：

   ```text
   yaml/config.yaml
   ```

   这个文件不入 git，通常包含模型、API key、工具、memory 和 runtime 配置。同步新机器或重装环境时，先看：

   ```text
   yaml/CHANGELOG.md
   ```

   里面记录了 `yaml/config.yaml` 的 shape 变化，以及每段配置在 Go 侧的读取位置。

3. **确认本地私有文件**

   常见本地文件包括：

   - `yaml/config.yaml`：本地模型与工具配置，不入 git。
   - `yaml/soul.md`：用户身份、偏好、长期注意事项，不入 git。
   - `.eino-cli/`：checkpoint、memory、安装后的二进制等运行时状态，不入 git。

### 运行 CLI

在仓库根目录运行：

```bash
go run .
```

也可以显式指定仓库根目录：

```bash
go run . --root /Users/bytedance/go/src/content/LLM
```

启动时 root 的解析优先级是：

1. `--root`
2. `SGADK_ROOT`
3. 当前工作目录

CLI 启动后会 `chdir` 到 root，确保配置、checkpoint、memory 和工具默认工作目录都落在同一个仓库下。

### 安装全局命令

执行一次安装脚本：

```bash
bash scripts/install-sgadk.sh
```

脚本会构建仓库内二进制到 `.eino-cli/bin/sgadk`，并把 wrapper 写入 `${HOME}/.local/bin/sgadk`。wrapper 会固定传入当前仓库 root，所以之后可以在任意目录运行：

```bash
sgadk
```

如果 `${HOME}/.local/bin` 不在 `PATH` 里，按脚本输出把它加进去。

如需安装到其他目录：

```bash
SGADK_INSTALL_DIR=/usr/local/bin bash scripts/install-sgadk.sh
```

## 核心特性

### TUI Agent 工作台

SGADK CLI 提供一个常驻终端界面，用来和 Deep Agent runtime 对话。普通回复会流式返回；模型工作中会显示类似 `Moonwalking...` 的 thinking indicator；输入 `/` 会弹出内置命令列表。

### 工具输出折叠

工具调用结果会作为独立 block 渲染到 scrollback，而不是只隐藏在 debug dump 里。长输出默认折叠，按 `Ctrl-O` 可以展开或收起最近一个可折叠 block。

### SOUL 长期人格

`/bootstrap` 会启动一段多轮 onboarding，对用户身份、偏好、代码风格、长期注意事项和禁止事项进行总结，并写入：

```text
yaml/soul.md
```

后续构建 system prompt 时会读取这个文件，把它作为长期上下文注入。

### Memory 与 Checkpoint

运行期状态默认落在 `.eino-cli/` 下。checkpoint 用来恢复 agent 运行状态；memory 用来保存长期事实和偏好。这个目录是本地状态，不应提交。

### 运行时 Reload

`/reload` 会在不退出 TUI 的情况下重建 agent/runner，并清空当前 TUI 对话视图。适合修改 `yaml/soul.md`、配置或 agent 代码后快速重启服务。

## TUI 命令

| 命令 | 作用 |
|---|---|
| `/bootstrap` | 创建或更新 `yaml/soul.md` |
| `/clear` | 清空当前内存对话历史 |
| `/debug [on\|off\|toggle]` | 开关原始模型输入/输出 trace |
| `/todos [open\|close\|toggle]` | 展开或折叠 todo 面板 |
| `/reload` | 重新启动 agent 服务 |
| `/help` | 查看内置帮助 |
| `/exit`, `/quit` | 退出 TUI |

常用快捷键：

- `Ctrl-O`：展开或折叠最近一个可折叠工具输出 block。
- `Esc`：模型回复中断当前请求；空闲时清空输入。
- `Ctrl-C`：回复中中断；空闲时按两次退出。

## 项目结构

```text
.
├── backend/
│   ├── agent/          # agent、prompt、middleware、tools
│   ├── cli/            # TUI 和 bootstrap 会话
│   ├── config/         # yaml config schema 和 loader
│   ├── memory/         # 长期记忆存储
│   └── runtime/        # Eino runtime 封装
├── scripts/            # 本地安装脚本
├── specs/              # 功能设计文档
├── yaml/               # 本地配置目录和 config shape changelog
├── main.go             # CLI 入口
└── go.mod
```

## 开发

运行测试：

```bash
go test ./...
```

构建：

```bash
go build -o /tmp/sgadk .
```

真实 TUI 交互验证可以直接运行二进制后手工输入命令：

```bash
go build -o /tmp/sgadk .
/tmp/sgadk --root /Users/bytedance/go/src/content/LLM
```

也可以用 `expect` 做伪终端冒烟测试，例如验证 `/reload` 是否能显示 `Agent service reloaded`。

## 安全与本地文件

- 不要提交 `yaml/config.yaml`、`yaml/soul.md` 或 `.eino-cli/`。
- 修改 `yaml/config.yaml` 的 shape 时，必须同步更新 `yaml/CHANGELOG.md`。
- `yaml/config.yaml` 常含 API key、临时调试值和本机路径；提交前必须检查 `git status`。
- `go.mod` 当前使用 `go 1.24.2`；如果 IDE linter 只支持 `go 1.23` 格式，可能会显示解析告警，但命令行 `go test ./...` 以本机 Go 版本为准。
