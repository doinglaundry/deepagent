package deepagent_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestDeepAgentDoesNotImportRemovedTopLevelPackages(t *testing.T) {
	forbidden := []string{
		"eino-cli/backend/",
		"eino-cli/host/",
		"eino-cli/manager/",
		"eino-cli/protocol/",
		"eino-cli/worker/",
	}

	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			for _, prefix := range forbidden {
				if importPath == prefix || strings.HasPrefix(importPath, prefix) {
					t.Errorf("%s imports removed top-level package %q", path, importPath)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorkerCommandDoesNotImportRemovedTopLevelPackages(t *testing.T) {
	root := filepath.Join("..", "cmd", "deepagent_worker")
	files, err := filepath.Glob(filepath.Join(root, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(importPath, "eino-cli/backend/") ||
				strings.HasPrefix(importPath, "eino-cli/host/") ||
				strings.HasPrefix(importPath, "eino-cli/manager/") ||
				strings.HasPrefix(importPath, "eino-cli/protocol/") ||
				strings.HasPrefix(importPath, "eino-cli/worker/") {
				t.Errorf("%s imports removed top-level package %q", path, importPath)
			}
		}
	}
}
