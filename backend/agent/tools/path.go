// Package tools holds the 7 built-in fs+shell tools injected via
// deep.Config.ToolsConfig.Tools (ls / read_file / write_file / edit_file /
// glob / grep / execute). Output formats and tool names mirror eino's
// adk/middlewares/filesystem 1:1 so prompts that target the model's
// pre-trained shape continue to work.
package tools

import (
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
