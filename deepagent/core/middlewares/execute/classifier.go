// Package execute classifies shell commands before they are executed.
package execute

import (
	"context"
	"path/filepath"
	"strings"
	"time"
)

type Classification string

const (
	ClassificationUnknown   Classification = "unknown"
	ClassificationSafe      Classification = "safe"
	ClassificationDangerous Classification = "dangerous"
	ClassificationForbidden Classification = "forbidden"
)

// CommandSpec is the single command description shared by policy and the
// executor. RawCommand remains available for conservative script analysis;
// Args is the tokenized form used by an executor.
type CommandSpec struct {
	Args          []string
	Command       string
	RawCommand    string
	WorkDir       string
	Timeout       time.Duration
	Env           map[string]string
	Justification string
}

type ClassificationResult struct {
	Classification Classification
	Reason         string
	FirstProgram   string
	SimpleCommands [][]string
}

type CommandClassifier interface {
	Classify(context.Context, CommandSpec) (ClassificationResult, error)
}

type DefaultClassifier struct{}

func NewDefaultClassifier() *DefaultClassifier { return &DefaultClassifier{} }

func (c *DefaultClassifier) Classify(ctx context.Context, spec CommandSpec) (ClassificationResult, error) {
	raw := strings.TrimSpace(spec.RawCommand)
	if raw == "" {
		return ClassificationResult{Classification: ClassificationUnknown, Reason: "no raw command"}, nil
	}
	return classifyShellScript(ctx, raw), nil
}

func classifyShellScript(ctx context.Context, script string) ClassificationResult {
	if containsShellSubstitution(script) {
		return ClassificationResult{Classification: ClassificationDangerous, Reason: "command substitution is not allowed"}
	}
	if containsOutputRedirection(script) {
		return ClassificationResult{Classification: ClassificationDangerous, Reason: "shell redirection or tee may write files"}
	}
	parts := splitShellScript(script)
	result := ClassificationResult{Classification: ClassificationSafe, Reason: "commands are on the safe list"}
	for _, part := range parts {
		args := tokenizeShell(part)
		if len(args) == 0 {
			continue
		}
		result.SimpleCommands = append(result.SimpleCommands, args)
		one := classifySimpleCommand(ctx, args)
		if severity(one.Classification) > severity(result.Classification) {
			result.Classification, result.Reason = one.Classification, one.Reason
		}
	}
	if len(result.SimpleCommands) == 0 {
		return ClassificationResult{Classification: ClassificationUnknown, Reason: "no commands found"}
	}
	result.FirstProgram = filepath.Base(result.SimpleCommands[0][0])
	return result
}

func classifySimpleCommand(ctx context.Context, args []string) ClassificationResult {
	if len(args) == 0 {
		return ClassificationResult{Classification: ClassificationUnknown, Reason: "empty command"}
	}
	program := filepath.Base(args[0])
	result := ClassificationResult{Classification: ClassificationUnknown, FirstProgram: program}
	switch program {
	case "bash", "sh", "dash", "zsh", "ksh", "csh":
		for i := 1; i < len(args); i++ {
			if args[i] == "-c" || args[i] == "-lc" || args[i] == "-cl" {
				if i+1 < len(args) {
					return classifyShellScript(ctx, args[i+1])
				}
				return result
			}
		}
		return result
	case "cat", "tail", "head", "ls", "nl", "grep", "egrep", "fgrep", "echo", "printf", "pwd", "whoami", "date", "true", "false":
		return ClassificationResult{Classification: ClassificationSafe, Reason: "read-only command", FirstProgram: program}
	case "rg":
		return classifyRG(args)
	case "sed":
		return classifySed(args)
	case "git":
		return classifyGit(args)
	case "find":
		return classifyFind(args)
	case "rm":
		for _, a := range args[1:] {
			if a == "/" || strings.HasPrefix(a, "/") && strings.Trim(a, "/") == "" {
				return ClassificationResult{Classification: ClassificationForbidden, Reason: "refusing to remove the root directory", FirstProgram: program}
			}
		}
		return ClassificationResult{Classification: ClassificationDangerous, Reason: "rm deletes files", FirstProgram: program}
	case "mv", "cp", "mkdir", "install", "touch":
		return ClassificationResult{Classification: ClassificationDangerous, Reason: "command may modify files", FirstProgram: program}
	case "xargs", "curl", "wget", "ssh", "scp":
		return ClassificationResult{Classification: ClassificationDangerous, Reason: "command may execute, transfer, or modify data", FirstProgram: program}
	case "python", "python3", "perl", "ruby", "node":
		for i := 1; i < len(args); i++ {
			if args[i] == "-c" {
				return ClassificationResult{Classification: ClassificationDangerous, Reason: "inline code execution", FirstProgram: program}
			}
		}
		return result
	default:
		return result
	}
}

func classifyRG(args []string) ClassificationResult {
	return ClassificationResult{Classification: ClassificationSafe, Reason: "rg is read-only by default", FirstProgram: filepath.Base(args[0])}
}
func classifySed(args []string) ClassificationResult {
	for _, a := range args[1:] {
		if strings.Contains(a, "i") && strings.HasPrefix(a, "-") {
			return ClassificationResult{Classification: ClassificationDangerous, Reason: "sed in-place editing modifies files", FirstProgram: filepath.Base(args[0])}
		}
	}
	return ClassificationResult{Classification: ClassificationSafe, Reason: "sed is read-only without in-place editing", FirstProgram: filepath.Base(args[0])}
}
func classifyGit(args []string) ClassificationResult {
	// Global configuration overrides can alter transports, hooks, and command
	// behavior, so they are conservative regardless of the subcommand.
	for _, arg := range args[1:] {
		if arg == "-c" || strings.HasPrefix(arg, "-c=") || arg == "--config-env" || strings.HasPrefix(arg, "--config-env=") {
			return ClassificationResult{Classification: ClassificationDangerous, Reason: "git configuration override may change command behavior", FirstProgram: "git"}
		}
	}
	if len(args) < 2 {
		return ClassificationResult{Classification: ClassificationUnknown, Reason: "git subcommand is missing", FirstProgram: "git"}
	}
	subcommand := args[1]
	switch subcommand {
	case "status", "log", "show", "diff", "branch", "ls-files", "rev-parse", "describe":
		if subcommand == "branch" {
			for _, arg := range args[2:] {
				if arg == "-d" || arg == "-D" || arg == "--delete" || arg == "-m" || arg == "-M" || arg == "--move" || arg == "-c" || arg == "-C" || arg == "--copy" {
					return ClassificationResult{Classification: ClassificationDangerous, Reason: "git branch may create, delete, or rename branches", FirstProgram: "git"}
				}
			}
		}
		// A diff can invoke an external helper when explicitly requested.
		for _, arg := range args[2:] {
			if arg == "--ext-diff" || arg == "--no-index" && len(args) > 2 {
				return ClassificationResult{Classification: ClassificationDangerous, Reason: "git diff may invoke external tools", FirstProgram: "git"}
			}
		}
		return ClassificationResult{Classification: ClassificationSafe, Reason: "read-only git command", FirstProgram: "git"}
	}
	return ClassificationResult{Classification: ClassificationDangerous, Reason: "git subcommand may modify the repository", FirstProgram: "git"}
}
func classifyFind(args []string) ClassificationResult {
	for _, a := range args[1:] {
		switch a {
		case "-delete", "-exec", "-execdir", "-ok", "-okdir", "-fls", "-fprint", "-fprint0", "-fprintf":
			return ClassificationResult{Classification: ClassificationDangerous, Reason: "find can execute commands or delete files", FirstProgram: "find"}
		}
	}
	return ClassificationResult{Classification: ClassificationSafe, Reason: "find is read-only without actions", FirstProgram: "find"}
}

func severity(c Classification) int {
	switch c {
	case ClassificationSafe:
		return 0
	case ClassificationModerate:
		return 1
	case ClassificationUnknown:
		return 2
	case ClassificationDangerous:
		return 3
	case ClassificationForbidden:
		return 4
	}
	return 2
}

func splitShellScript(s string) []string {
	var out []string
	var b strings.Builder
	var single, double, esc bool
	r := []rune(s)
	flush := func() { appendShellPart(&out, &b) }
	for i := 0; i < len(r); i++ {
		ch := r[i]
		if esc {
			b.WriteRune(ch)
			esc = false
			continue
		}
		if ch == '\\' && !single {
			esc = true
			b.WriteRune(ch)
			continue
		}
		if ch == '\'' && !double {
			single = !single
			b.WriteRune(ch)
			continue
		}
		if ch == '"' && !single {
			double = !double
			b.WriteRune(ch)
			continue
		}
		if !single && !double && (ch == ';' || ch == '|' || ch == '&' || ch == '\n') {
			flush()
			if (ch == '|' || ch == '&') && i+1 < len(r) && r[i+1] == ch {
				i++
			}
			continue
		}
		b.WriteRune(ch)
	}
	flush()
	return out
}

func appendShellPart(parts *[]string, b *strings.Builder) {
	if parts == nil || b == nil {
		return
	}
	if part := strings.TrimSpace(b.String()); part != "" {
		*parts = append(*parts, part)
	}
	b.Reset()
}

func containsShellSubstitution(s string) bool {
	var single, double, esc bool
	r := []rune(s)
	for i := 0; i < len(r); i++ {
		ch := r[i]
		if esc {
			esc = false
			continue
		}
		if ch == '\\' && !single {
			esc = true
			continue
		}
		if ch == '\'' && !double {
			single = !single
			continue
		}
		if ch == '"' && !single {
			double = !double
			continue
		}
		if !single && (ch == '`' || (ch == '$' && i+1 < len(r) && r[i+1] == '(')) {
			return true
		}
	}
	return false
}

func containsOutputRedirection(s string) bool {
	var single, double, esc bool
	for _, ch := range []rune(s) {
		if esc {
			esc = false
			continue
		}
		if ch == '\\' && !single {
			esc = true
			continue
		}
		if ch == '\'' && !double {
			single = !single
			continue
		}
		if ch == '"' && !single {
			double = !double
			continue
		}
		if !single && !double && ch == '>' {
			return true
		}
	}
	return false
}

func tokenizeShellWords(s string) []string { return tokenizeShell(s) }

func shellInlineScript(args []string) (string, bool) {
	for i, arg := range args {
		if (arg == "-c" || arg == "-lc" || arg == "-cl") && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func containsArg(args []string, value string) bool {
	for _, arg := range args {
		if arg == value {
			return true
		}
	}
	return false
}

func rmTargetsRoot(args []string) bool {
	for _, arg := range args {
		clean := filepath.Clean(arg)
		if clean == "/" {
			return true
		}
	}
	return false
}

func tokenizeShell(s string) []string {
	var out []string
	var b strings.Builder
	var single, double, esc bool
	flush := func() {
		if b.Len() > 0 {
			out = append(out, b.String())
			b.Reset()
		}
	}
	for _, ch := range []rune(s) {
		if esc {
			b.WriteRune(ch)
			esc = false
			continue
		}
		if ch == '\\' && !single {
			esc = true
			continue
		}
		if ch == '\'' && !double {
			single = !single
			continue
		}
		if ch == '"' && !single {
			double = !double
			continue
		}
		if !single && !double && (ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n') {
			flush()
			continue
		}
		b.WriteRune(ch)
	}
	flush()
	return out
}
