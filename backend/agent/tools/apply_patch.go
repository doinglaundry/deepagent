package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

const applyPatchToolDesc = `Apply a file-oriented patch. Supports Add File and Update File hunks with exact context matching. All paths must stay inside the workspace.`

type applyPatchArgs struct {
	Patch string `json:"patch" jsonschema:"required,description=Patch text in the repository patch format"`
}

type patchFileOp struct {
	kind  string
	path  string
	lines []string
	hunks [][]patchLine
}

type patchLine struct {
	kind byte
	text string
}

func GetApplyPatchTool(root string) (tool.BaseTool, error) {
	return utils.InferTool("apply_patch", applyPatchToolDesc,
		func(ctx context.Context, in applyPatchArgs) (string, error) {
			if msg, denied := denyOnPlanMode(ctx); denied {
				return msg, nil
			}
			// Sandbox path: apply_patch is multi-file; we don't route the
			// whole batch through Sandbox.* yet (no atomic "write set"
			// primitive). Fall through to host fs — sandboxed deployments
			// configure mounts so /mnt/... paths resolve to host dirs the
			// process owns anyway.
			return applyPatch(root, in.Patch)
		})
}

func applyPatch(root, patch string) (string, error) {
	ops, err := parsePatch(patch)
	if err != nil {
		return "", err
	}
	writes := make(map[string]string, len(ops))
	for _, op := range ops {
		path, err := getResolvedPath(root, op.path)
		if err != nil {
			return "", err
		}
		content, err := buildPatchedContent(path, op)
		if err != nil {
			return "", err
		}
		writes[path] = content
	}
	for path, content := range writes {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("Applied patch to %d file(s)", len(writes)), nil
}

func buildPatchedContent(path string, op patchFileOp) (string, error) {
	switch op.kind {
	case "add":
		if _, err := os.Stat(path); err == nil {
			return "", fmt.Errorf("file already exists: %s", path)
		} else if !os.IsNotExist(err) {
			return "", err
		}
		return strings.Join(op.lines, "\n"), nil
	case "update":
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		lines := strings.Split(string(data), "\n")
		for _, hunk := range op.hunks {
			var oldLines []string
			var newLines []string
			for _, line := range hunk {
				switch line.kind {
				case ' ':
					oldLines = append(oldLines, line.text)
					newLines = append(newLines, line.text)
				case '-':
					oldLines = append(oldLines, line.text)
				case '+':
					newLines = append(newLines, line.text)
				}
			}
			index, err := findUniqueLineBlock(lines, oldLines)
			if err != nil {
				return "", err
			}
			next := make([]string, 0, len(lines)-len(oldLines)+len(newLines))
			next = append(next, lines[:index]...)
			next = append(next, newLines...)
			next = append(next, lines[index+len(oldLines):]...)
			lines = next
		}
		return strings.Join(lines, "\n"), nil
	default:
		return "", fmt.Errorf("unsupported patch operation: %s", op.kind)
	}
}

func parsePatch(patch string) ([]patchFileOp, error) {
	lines := strings.Split(strings.ReplaceAll(patch, "\r\n", "\n"), "\n")
	if len(lines) < 2 || lines[0] != "*** Begin Patch" {
		return nil, fmt.Errorf("patch must start with *** Begin Patch")
	}
	var ops []patchFileOp
	var current *patchFileOp
	for i := 1; i < len(lines); i++ {
		line := lines[i]
		switch {
		case line == "*** End Patch":
			if current != nil {
				ops = append(ops, *current)
			}
			if len(ops) == 0 {
				return nil, fmt.Errorf("patch has no file operations")
			}
			return ops, nil
		case strings.HasPrefix(line, "*** Add File: "):
			if current != nil {
				ops = append(ops, *current)
			}
			current = &patchFileOp{kind: "add", path: strings.TrimPrefix(line, "*** Add File: ")}
		case strings.HasPrefix(line, "*** Update File: "):
			if current != nil {
				ops = append(ops, *current)
			}
			current = &patchFileOp{kind: "update", path: strings.TrimPrefix(line, "*** Update File: ")}
		case line == "@@" || strings.HasPrefix(line, "@@ "):
			if current == nil || current.kind != "update" {
				return nil, fmt.Errorf("hunk without update file")
			}
			current.hunks = append(current.hunks, nil)
		default:
			if current == nil {
				if strings.TrimSpace(line) == "" {
					continue
				}
				return nil, fmt.Errorf("patch line outside file operation: %q", line)
			}
			if current.kind == "add" {
				if !strings.HasPrefix(line, "+") {
					return nil, fmt.Errorf("add file lines must start with +")
				}
				current.lines = append(current.lines, strings.TrimPrefix(line, "+"))
				continue
			}
			if len(current.hunks) == 0 {
				return nil, fmt.Errorf("update line outside hunk")
			}
			if line == "" {
				return nil, fmt.Errorf("empty hunk line must include a prefix")
			}
			prefix := line[0]
			if prefix != ' ' && prefix != '-' && prefix != '+' {
				return nil, fmt.Errorf("invalid hunk line prefix: %q", line)
			}
			last := len(current.hunks) - 1
			current.hunks[last] = append(current.hunks[last], patchLine{kind: prefix, text: line[1:]})
		}
	}
	return nil, fmt.Errorf("patch must end with *** End Patch")
}

func findUniqueLineBlock(lines, want []string) (int, error) {
	matches := 0
	index := -1
	for i := 0; i+len(want) <= len(lines); i++ {
		if equalLines(lines[i:i+len(want)], want) {
			matches++
			index = i
		}
	}
	if matches == 0 {
		return 0, fmt.Errorf("patch context not found")
	}
	if matches > 1 {
		return 0, fmt.Errorf("patch context matched %d times", matches)
	}
	return index, nil
}

func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
