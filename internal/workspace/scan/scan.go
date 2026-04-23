package scan

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Result struct {
	RootPath        string
	IsGitRepo       bool
	LanguageStack   []string
	EntryFiles      []string
	ConfigFiles     []string
	DependencyFiles []string
	ScanTimestamp   time.Time
}

func Detect(root string) (Result, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return Result{}, err
	}

	result := Result{RootPath: absRoot, ScanTimestamp: time.Now()}
	languages := map[string]struct{}{}

	if _, err := os.Stat(filepath.Join(absRoot, ".git")); err == nil {
		result.IsGitRepo = true
	}

	err = filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path != absRoot && d.IsDir() {
			name := d.Name()
			if name == ".git" || name == ".idea" || name == ".ttadk" || name == ".claude" {
				return filepath.SkipDir
			}
		}
		if d.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(absRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		base := filepath.Base(path)
		ext := strings.ToLower(filepath.Ext(base))

		switch ext {
		case ".go":
			languages["go"] = struct{}{}
		case ".py":
			languages["python"] = struct{}{}
		case ".ts", ".tsx", ".js", ".jsx":
			languages["javascript"] = struct{}{}
		case ".rs":
			languages["rust"] = struct{}{}
		}

		switch base {
		case "go.mod", "package.json", "pyproject.toml", "Cargo.toml", "pom.xml", "Makefile":
			result.DependencyFiles = append(result.DependencyFiles, rel)
		}

		switch base {
		case ".env", ".env.example", ".mcp.json", "settings.json", "settings.local.json":
			result.ConfigFiles = append(result.ConfigFiles, rel)
		}

		if strings.HasPrefix(rel, "cmd/") || strings.HasPrefix(rel, "main.") {
			result.EntryFiles = append(result.EntryFiles, rel)
		}

		return nil
	})
	if err != nil {
		return Result{}, err
	}

	for language := range languages {
		result.LanguageStack = append(result.LanguageStack, language)
	}

	sort.Strings(result.LanguageStack)
	sort.Strings(result.EntryFiles)
	sort.Strings(result.ConfigFiles)
	sort.Strings(result.DependencyFiles)

	return result, nil
}
