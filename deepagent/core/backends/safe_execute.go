package backends

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// SafeExecuteBackend 安全命令执行后端
// 包装 SandboxBackend，限制 Execute 只允许执行白名单中的命令
//
// Deprecated: use execute middleware policy instead.
type SafeExecuteBackend struct {
	SandboxBackend
	allowedCommands map[string]bool
}

// SafeExecuteConfig 安全执行配置
type SafeExecuteConfig struct {
	// AllowedCommands 允许执行的命令白名单（命令名，不含路径）
	// 例如: ["dreamina-cli", "ffmpeg", "ffprobe"]
	AllowedCommands []string
}

// NewSafeExecuteBackend 创建安全命令执行后端
//
// Deprecated: use execute middleware policy instead.
func NewSafeExecuteBackend(inner SandboxBackend, cfg *SafeExecuteConfig) *SafeExecuteBackend {
	allowed := make(map[string]bool, len(cfg.AllowedCommands))
	for _, cmd := range cfg.AllowedCommands {
		allowed[cmd] = true
	}
	return &SafeExecuteBackend{SandboxBackend: inner, allowedCommands: allowed}
}

// Execute 执行 shell 命令（带白名单检查）
func (b *SafeExecuteBackend) Execute(ctx context.Context, command string) (*ExecuteResponse, error) {
	if err := b.validateCommand(command); err != nil {
		return &ExecuteResponse{Output: err.Error(), ExitCode: 1}, nil
	}
	return b.SandboxBackend.Execute(ctx, command)
}
func (b *SafeExecuteBackend) ExecuteCommand(ctx context.Context, req CommandRequest) (*CommandResult, error) {
	if err := b.validateCommand(req.Command); err != nil {
		return &CommandResult{Output: err.Error(), ExitCode: 1}, nil
	}
	return b.SandboxBackend.ExecuteCommand(ctx, req)
}

// validateCommand 校验命令是否在白名单中
func (b *SafeExecuteBackend) validateCommand(command string) error {
	// 检查危险的 shell 特性：命令替换、进程替换
	if containsShellSubstitution(command) {
		return fmt.Errorf("command rejected: shell substitution ($(), ``) is not allowed")
	}
	// 按 shell 操作符拆分子命令
	subCommands := splitByShellOperators(command)
	for _, sub := range subCommands {
		sub = strings.TrimSpace(sub)
		if sub == "" {
			continue
		}
		// 跳过纯重定向片段
		if strings.HasPrefix(sub, ">") || strings.HasPrefix(sub, "<") {
			continue
		}
		cmdName := extractCommandName(sub)
		if cmdName == "" {
			continue
		}
		// 取 basename，支持 /usr/bin/ffmpeg 等完整路径
		baseName := filepath.Base(cmdName)
		if !b.allowedCommands[baseName] {
			return fmt.Errorf("command rejected: '%s' is not in the allowed command list. Allowed commands: %s", baseName, b.allowedCommandList())
		}
	}
	return nil
}

// containsShellSubstitution 检查是否包含 shell 命令替换
func containsShellSubstitution(command string) bool {
	if strings.Contains(command, "$(") {
		return true
	}
	if strings.Contains(command, "`") {
		return true
	}
	return false
}

// splitByShellOperators 按 shell 操作符拆分命令
// 支持: &&, ||, ;, |, &
func splitByShellOperators(command string) []string {
	var parts []string
	var current strings.Builder
	inSingleQuote := false
	inDoubleQuote := false
	runes := []rune(command)
	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		if ch == '\'' && !inDoubleQuote {
			inSingleQuote = !inSingleQuote
			current.WriteRune(ch)
			continue
		}
		if ch == '"' && !inSingleQuote {
			inDoubleQuote = !inDoubleQuote
			current.WriteRune(ch)
			continue
		}
		if inSingleQuote || inDoubleQuote {
			current.WriteRune(ch)
			continue
		}
		if i+1 < len(runes) {
			twoChar := string(runes[i : i+2])
			if twoChar == "&&" || twoChar == "||" {
				parts = append(parts, current.String())
				current.Reset()
				i++
				continue
			}
		}
		if ch == ';' || ch == '|' || ch == '&' {
			parts = append(parts, current.String())
			current.Reset()
			continue
		}
		current.WriteRune(ch)
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts
}

// extractCommandName 从子命令中提取可执行程序名
func extractCommandName(subCommand string) string {
	subCommand = strings.TrimSpace(subCommand)
	// 跳过前导的环境变量赋值 (KEY=VALUE cmd ...)
	fields := strings.Fields(subCommand)
	for _, f := range fields {
		if strings.Contains(f, "=") && !strings.HasPrefix(f, "-") {
			continue
		}
		return f
	}
	return ""
}

// allowedCommandList 返回允许的命令列表字符串
func (b *SafeExecuteBackend) allowedCommandList() string {
	cmds := make([]string, 0, len(b.allowedCommands))
	for cmd := range b.allowedCommands {
		cmds = append(cmds, cmd)
	}
	return strings.Join(cmds, ", ")
}

// 确保实现接口
var _ SandboxBackend = (*SafeExecuteBackend)(nil)
