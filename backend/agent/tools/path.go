// Package tools holds the built-in fs+shell tools injected via
// deep.Config.ToolsConfig.Tools. Existing tool names stay registered while
// Cursor-compatible tools are added alongside them.
package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolveRoot returns root, falling back to os.Getwd then "." when empty.
// Mirrors legacy fs_backend.resolveRoot so existing test fixtures behave
// identically post-migration.
func resolveRoot(root string) string {
	if strings.TrimSpace(root) != "" {
		return root
	}
	if cwd, err := os.Getwd(); err == nil && cwd != "" {
		return cwd
	}
	return "."
}

// resolvePath joins p against the resolved root unless p is already absolute.
func resolvePath(root, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(resolveRoot(root), p)
}

func getResolvedPath(root, p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("path must not be empty")
	}
	base, err := filepath.Abs(resolveRoot(root))
	if err != nil {
		return "", err
	}
	path := p
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	path, err = filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	if !isInsideRoot(base, path) {
		return "", fmt.Errorf("path escapes root: %s", p)
	}
	return path, nil
}

func isInsideRoot(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}

func truncateToolOutput(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes] + fmt.Sprintf("\n[output truncated: %d bytes omitted]", len(s)-maxBytes)
}
