package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

const readLintsToolDesc = `Read local diagnostics for files or directories. This CLI version approximates IDE diagnostics with go test for Go packages and reports unsupported providers for other file types.`

type readLintsArgs struct {
	Paths []string `json:"paths,omitempty" jsonschema:"description=Optional files or directories to check"`
}

func GetReadLintsTool() (tool.BaseTool, error) {
	return utils.InferTool("read_lints", readLintsToolDesc,
		func(ctx context.Context, in readLintsArgs) (string, error) {
			return readLints(ctx, resolveRoot(), in.Paths)
		})
}

func readLints(ctx context.Context, root string, paths []string) (string, error) {
	packages, unsupported, err := getLintTargets(root, paths)
	if err != nil {
		return "", err
	}
	if len(packages) == 0 {
		if len(unsupported) == 0 {
			return "No diagnostics", nil
		}
		return strings.Join(unsupported, "\n"), nil
	}
	args := append([]string{"test"}, packages...)
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = resolveRoot()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return truncateToolOutput(strings.TrimRight(string(out), "\n"), 64*1024), nil
	}
	if len(unsupported) > 0 {
		return "No diagnostics\n" + strings.Join(unsupported, "\n"), nil
	}
	return "No diagnostics", nil
}

func getLintTargets(root string, paths []string) ([]string, []string, error) {
	if len(paths) == 0 {
		return []string{"./..."}, nil, nil
	}
	seen := map[string]bool{}
	var packages []string
	var unsupported []string
	for _, p := range paths {
		path, err := getResolvedPath(p)
		if err != nil {
			return nil, nil, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, nil, err
		}
		target := path
		if !info.IsDir() {
			if filepath.Ext(path) != ".go" {
				unsupported = append(unsupported, "No diagnostics provider for "+path)
				continue
			}
			target = filepath.Dir(path)
		}
		base, err := filepath.Abs(resolveRoot())
		if err != nil {
			return nil, nil, err
		}
		relPath, err := filepath.Rel(base, target)
		if err != nil {
			return nil, nil, err
		}
		rel := "./" + filepath.ToSlash(relPath)
		if rel == "./." {
			rel = "."
		}
		if info.IsDir() {
			rel = strings.TrimSuffix(rel, "/") + "/..."
		}
		if !seen[rel] {
			seen[rel] = true
			packages = append(packages, rel)
		}
	}
	return packages, unsupported, nil
}
