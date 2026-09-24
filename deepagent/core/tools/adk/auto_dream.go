package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/cloudwego/eino/adk/middlewares/filesystem"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"eino-cli/deepagent/sandbox"
	"eino-cli/deepagent/sandbox/paths"
)

const autoDreamShellDenied = "auto-dream shell only allows read-only commands: ls, pwd, rg, grep"

const shellToolDesc = `Execute a read-only command in the auto-dream workspace.`

type shellArgs struct {
	Command    string `json:"command" jsonschema:"required,description=Read-only shell command"`
	WorkingDir string `json:"working_directory,omitempty"`
}

type writeFileArgs struct {
	FilePath string `json:"file_path" jsonschema:"description=The path to the file to write"`
	Content  string `json:"content" jsonschema:"description=The content to write to the file"`
}

type editFileArgs struct {
	FilePath   string `json:"file_path" jsonschema:"description=The path to the file to modify"`
	OldString  string `json:"old_string" jsonschema:"description=The text to replace"`
	NewString  string `json:"new_string" jsonschema:"description=The text to replace it with"`
	ReplaceAll bool   `json:"replace_all" jsonschema:"description=Replace all occurrences of old_string"`
}

func applyEditReplacement(content, oldStr, newStr string, replaceAll bool) (string, error) {
	if replaceAll {
		return strings.ReplaceAll(content, oldStr, newStr), nil
	}
	count := strings.Count(content, oldStr)
	if count == 0 {
		return "", fmt.Errorf("old_string not found in file")
	}
	if count > 1 {
		return "", fmt.Errorf("old_string appears %d times; set replace_all=true or make it unique", count)
	}
	return strings.Replace(content, oldStr, newStr, 1), nil
}

func isReadOnlyShellCommand(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" || strings.ContainsAny(command, ";&|><`$") {
		return false
	}
	name := strings.Fields(command)[0]
	switch name {
	case "ls", "pwd", "rg", "grep":
		return true
	}
	return false
}

func GetAutoDreamShellTool(sandboxManager sandbox.SandboxManager) (tool.BaseTool, error) {
	return utils.InferTool("shell", shellToolDesc,
		func(ctx context.Context, in shellArgs) (string, error) {
			if !isReadOnlyShellCommand(in.Command) {
				return autoDreamShellDenied, nil
			}
			if sb := getSandbox(ctx, sandboxManager); sb != nil {
				mappings, err := sandboxpaths.BuildMountMappings(sb.SessionID())
				if err != nil {
					return "", err
				}
				command, err := buildSandboxCommand(in.Command, in.WorkingDir, mappings)
				if err != nil {
					return "", err
				}
				return sb.ExecuteCommand(ctx, command)
			}
			workingDir := resolveRoot()
			if strings.TrimSpace(in.WorkingDir) != "" {
				var err error
				workingDir, err = getResolvedPath(in.WorkingDir)
				if err != nil {
					return "", err
				}
			}
			cmd := exec.CommandContext(ctx, "/bin/bash", "-c", in.Command)
			cmd.Dir = workingDir
			output, err := cmd.CombinedOutput()
			return string(output), err
		})
}

func GetAutoDreamWriteFileTool(memoryRoot string) (tool.BaseTool, error) {
	return utils.InferTool(filesystem.ToolNameWriteFile, filesystem.WriteFileToolDesc,
		func(_ context.Context, in writeFileArgs) (string, error) {
			path, err := getAutoDreamPath(memoryRoot, in.FilePath)
			if err != nil {
				return "", err
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return "", err
			}
			if err := os.WriteFile(path, []byte(in.Content), 0o644); err != nil {
				return "", err
			}
			return fmt.Sprintf("Updated file %s", path), nil
		})
}

func GetAutoDreamEditFileTool(memoryRoot string) (tool.BaseTool, error) {
	return utils.InferTool(filesystem.ToolNameEditFile, filesystem.EditFileToolDesc,
		func(_ context.Context, in editFileArgs) (string, error) {
			if in.OldString == "" {
				return "", fmt.Errorf("old_string must not be empty")
			}
			path, err := getAutoDreamPath(memoryRoot, in.FilePath)
			if err != nil {
				return "", err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return "", err
			}
			updated, err := applyEditReplacement(string(data), in.OldString, in.NewString, in.ReplaceAll)
			if err != nil {
				return "", err
			}
			if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
				return "", err
			}
			return fmt.Sprintf("Successfully replaced the string in '%s'", path), nil
		})
}

func getAutoDreamPath(memoryRoot, inputPath string) (string, error) {
	path := inputPath
	if !filepath.IsAbs(path) {
		path = filepath.Join(memoryRoot, path)
	}
	if !isInsidePath(memoryRoot, path) {
		return "", fmt.Errorf("auto-dream may only write inside %s", memoryRoot)
	}
	return filepath.Clean(path), nil
}

func isInsidePath(root, path string) bool {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(rootAbs), filepath.Clean(pathAbs))
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel))
}
