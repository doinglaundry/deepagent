package middleware

import (
	"context"
	"path/filepath"
	"strings"
)

type commandClassification string

const (
	classificationUnknown   commandClassification = "unknown"
	classificationSafe      commandClassification = "safe"
	classificationDangerous commandClassification = "dangerous"
	classificationForbidden commandClassification = "forbidden"
)

type classificationResult struct {
	Classification commandClassification
	Reason         string
	FirstProgram   string
	SimpleCommands [][]string
}

func classifyShellScript(ctx context.Context, script string) classificationResult {
	if containsShellSubstitution(script) {
		return classificationResult{Classification: classificationDangerous, Reason: "command substitution is not allowed"}
	}
	if containsOutputRedirection(script) {
		return classificationResult{Classification: classificationDangerous, Reason: "shell redirection or tee may write files"}
	}
	parts := splitShellScript(script)
	result := classificationResult{Classification: classificationSafe, Reason: "commands are on the safe list"}
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
		return classificationResult{Classification: classificationUnknown, Reason: "no commands found"}
	}
	result.FirstProgram = filepath.Base(result.SimpleCommands[0][0])
	return result
}

func classifySimpleCommand(ctx context.Context, args []string) classificationResult {
	if len(args) == 0 {
		return classificationResult{Classification: classificationUnknown, Reason: "empty command"}
	}
	program := filepath.Base(args[0])
	result := classificationResult{Classification: classificationUnknown, FirstProgram: program}
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
		return classificationResult{Classification: classificationSafe, Reason: "read-only command", FirstProgram: program}
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
				return classificationResult{Classification: classificationForbidden, Reason: "refusing to remove the root directory", FirstProgram: program}
			}
		}
		return classificationResult{Classification: classificationDangerous, Reason: "rm deletes files", FirstProgram: program}
	case "mv", "cp", "mkdir", "install", "touch":
		return classificationResult{Classification: classificationDangerous, Reason: "command may modify files", FirstProgram: program}
	case "xargs", "curl", "wget", "ssh", "scp":
		return classificationResult{Classification: classificationDangerous, Reason: "command may execute, transfer, or modify data", FirstProgram: program}
	case "python", "python3", "perl", "ruby", "node":
		for i := 1; i < len(args); i++ {
			if args[i] == "-c" {
				return classificationResult{Classification: classificationDangerous, Reason: "inline code execution", FirstProgram: program}
			}
		}
		return result
	default:
		return result
	}
}

func classifyRG(args []string) classificationResult {
	return classificationResult{Classification: classificationSafe, Reason: "rg is read-only by default", FirstProgram: filepath.Base(args[0])}
}
func classifySed(args []string) classificationResult {
	for _, a := range args[1:] {
		if strings.Contains(a, "i") && strings.HasPrefix(a, "-") {
			return classificationResult{Classification: classificationDangerous, Reason: "sed in-place editing modifies files", FirstProgram: filepath.Base(args[0])}
		}
	}
	return classificationResult{Classification: classificationSafe, Reason: "sed is read-only without in-place editing", FirstProgram: filepath.Base(args[0])}
}
func classifyGit(args []string) classificationResult {
	// Global configuration overrides can alter transports, hooks, and command
	// behavior, so they are conservative regardless of the subcommand.
	for _, arg := range args[1:] {
		if arg == "-c" || strings.HasPrefix(arg, "-c=") || arg == "--config-env" || strings.HasPrefix(arg, "--config-env=") {
			return classificationResult{Classification: classificationDangerous, Reason: "git configuration override may change command behavior", FirstProgram: "git"}
		}
	}
	if len(args) < 2 {
		return classificationResult{Classification: classificationUnknown, Reason: "git subcommand is missing", FirstProgram: "git"}
	}
	subcommand := args[1]
	switch subcommand {
	case "status", "log", "show", "diff", "branch", "ls-files", "rev-parse", "describe":
		if subcommand == "branch" {
			return classifyGitBranch(args[2:])
		}
		// A diff can invoke an external helper when explicitly requested.
		for _, arg := range args[2:] {
			if arg == "--ext-diff" || arg == "--no-index" && len(args) > 2 {
				return classificationResult{Classification: classificationDangerous, Reason: "git diff may invoke external tools", FirstProgram: "git"}
			}
		}
		return classificationResult{Classification: classificationSafe, Reason: "read-only git command", FirstProgram: "git"}
	}
	return classificationResult{Classification: classificationDangerous, Reason: "git subcommand may modify the repository", FirstProgram: "git"}
}

// Branch has both query and mutation forms. Positional arguments are names
// to create unless an explicit listing/filter option selects query mode.
func classifyGitBranch(args []string) classificationResult {
	denied := classificationResult{Classification: classificationDangerous, Reason: "git branch may change branches or their configuration", FirstProgram: "git"}
	listing, positional := false, false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = positional || i+1 < len(args)
			break
		}
		if !strings.HasPrefix(arg, "-") {
			positional = true
			continue
		}
		if strings.HasPrefix(arg, "--") {
			key, _, hasValue := strings.Cut(arg, "=")
			switch key {
			case "--list", "--all", "--remotes", "--show-current":
				if hasValue {
					return denied
				}
				listing = true
			case "--verbose", "--quiet", "--ignore-case", "--no-color", "--no-column":
				if hasValue {
					return denied
				}
			case "--color", "--column", "--abbrev", "--no-abbrev":
				// Optional values must be attached using '='.
			case "--contains", "--no-contains", "--merged", "--no-merged", "--points-at", "--sort", "--format":
				if key != "--sort" && key != "--format" {
					listing = true
				}
				if !hasValue && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
					i++
				}
			default:
				return denied
			}
			continue
		}
		if arg == "-" {
			return denied
		}
		for _, option := range arg[1:] {
			switch option {
			case 'a', 'r', 'l':
				listing = true
			case 'v', 'q', 'i':
			default:
				return denied
			}
		}
	}
	if positional && !listing {
		return denied
	}
	return classificationResult{Classification: classificationSafe, Reason: "read-only git branch query", FirstProgram: "git"}
}

func classifyFind(args []string) classificationResult {
	for _, a := range args[1:] {
		switch a {
		case "-delete", "-exec", "-execdir", "-ok", "-okdir", "-fls", "-fprint", "-fprint0", "-fprintf":
			return classificationResult{Classification: classificationDangerous, Reason: "find can execute commands or delete files", FirstProgram: "find"}
		}
	}
	return classificationResult{Classification: classificationSafe, Reason: "find is read-only without actions", FirstProgram: "find"}
}

func severity(c commandClassification) int {
	switch c {
	case classificationSafe:
		return 0
	case classificationUnknown:
		return 2
	case classificationDangerous:
		return 3
	case classificationForbidden:
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
