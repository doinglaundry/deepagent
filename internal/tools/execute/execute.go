package execute

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"eino-cli/internal/tools"
)

type Executor struct{}

func New() *Executor {
	return &Executor{}
}

func (e *Executor) Execute(tool tools.Tool, args []string, cwd string) (tools.Result, error) {
	switch tool.Name {
	case "read":
		return e.readFile(args, cwd)
	case "ls":
		return e.listDir(args, cwd)
	case "shell":
		return e.runShell(args, cwd)
	default:
		return tools.Result{}, fmt.Errorf("unsupported tool: %s", tool.Name)
	}
}

func (e *Executor) readFile(args []string, cwd string) (tools.Result, error) {
	if len(args) == 0 {
		return tools.Result{}, fmt.Errorf("read requires a file path")
	}
	path := resolvePath(cwd, args[0])
	content, err := os.ReadFile(path)
	if err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Output: string(content)}, nil
}

func (e *Executor) listDir(args []string, cwd string) (tools.Result, error) {
	target := cwd
	if len(args) > 0 {
		target = resolvePath(cwd, args[0])
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return tools.Result{}, err
	}
	items := make([]string, 0, len(entries))
	for _, entry := range entries {
		items = append(items, entry.Name())
	}
	return tools.Result{Output: strings.Join(items, "\n")}, nil
}

func (e *Executor) runShell(args []string, cwd string) (tools.Result, error) {
	if len(args) == 0 {
		return tools.Result{}, fmt.Errorf("shell requires a command")
	}
	command := exec.Command("bash", "-lc", strings.Join(args, " "))
	command.Dir = cwd
	output, err := command.CombinedOutput()
	if err != nil {
		return tools.Result{Output: string(output)}, err
	}
	return tools.Result{Output: string(output)}, nil
}

func resolvePath(cwd, value string) string {
	if filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(cwd, value)
}
