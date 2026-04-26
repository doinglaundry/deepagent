package eino

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/cloudwego/eino/adk/filesystem"
)

type localBackend struct{ root string }

func newLocalBackend(root string) filesystem.Backend {
	if root == "" {
		root, _ = os.Getwd()
	}
	return &localBackend{root: root}
}

func (b *localBackend) LsInfo(ctx context.Context, req *filesystem.LsInfoRequest) ([]filesystem.FileInfo, error) {
	dir := req.Path
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(b.root, dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	result := make([]filesystem.FileInfo, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		result = append(result, filesystem.FileInfo{
			Path:       e.Name(),
			IsDir:      e.IsDir(),
			Size:       info.Size(),
			ModifiedAt: info.ModTime().UTC().Format(time.RFC3339),
		})
	}
	return result, nil
}

func (b *localBackend) Read(ctx context.Context, req *filesystem.ReadRequest) (*filesystem.FileContent, error) {
	p := req.FilePath
	if !filepath.IsAbs(p) {
		p = filepath.Join(b.root, p)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	content := string(data)
	if req.Offset > 0 || req.Limit > 0 {
		lines := strings.Split(content, "\n")
		start := req.Offset - 1
		if start < 0 {
			start = 0
		}
		if start >= len(lines) {
			return &filesystem.FileContent{Content: ""}, nil
		}
		end := len(lines)
		if req.Limit > 0 && start+req.Limit < end {
			end = start + req.Limit
		}
		content = strings.Join(lines[start:end], "\n")
	}
	return &filesystem.FileContent{Content: content}, nil
}

func (b *localBackend) GrepRaw(ctx context.Context, req *filesystem.GrepRequest) ([]filesystem.GrepMatch, error) {
	pattern := req.Pattern
	if req.CaseInsensitive {
		pattern = "(?i:" + pattern + ")"
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid pattern: %w", err)
	}

	searchRoot := b.root
	if req.Path != "" {
		if filepath.IsAbs(req.Path) {
			searchRoot = req.Path
		} else {
			searchRoot = filepath.Join(b.root, req.Path)
		}
	}

	var matches []filesystem.GrepMatch
	err = filepath.WalkDir(searchRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		rel, _ := filepath.Rel(b.root, path)
		for i, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				matches = append(matches, filesystem.GrepMatch{
					Path:    rel,
					Line:    i + 1,
					Content: line,
				})
			}
		}
		return nil
	})
	return matches, err
}

func (b *localBackend) GlobInfo(ctx context.Context, req *filesystem.GlobInfoRequest) ([]filesystem.FileInfo, error) {
	base := b.root
	if req.Path != "" {
		if filepath.IsAbs(req.Path) {
			base = req.Path
		} else {
			base = filepath.Join(b.root, req.Path)
		}
	}
	pattern := filepath.Join(base, req.Pattern)
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	result := make([]filesystem.FileInfo, 0, len(paths))
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(b.root, p)
		result = append(result, filesystem.FileInfo{
			Path:       rel,
			IsDir:      info.IsDir(),
			Size:       info.Size(),
			ModifiedAt: info.ModTime().UTC().Format(time.RFC3339),
		})
	}
	return result, nil
}

func (b *localBackend) Write(ctx context.Context, req *filesystem.WriteRequest) error {
	p := req.FilePath
	if !filepath.IsAbs(p) {
		p = filepath.Join(b.root, p)
	}
	err := os.MkdirAll(filepath.Dir(p), 0755)
	if err != nil {
		return err
	}
	return os.WriteFile(p, []byte(req.Content), 0644)
}

func (b *localBackend) Edit(ctx context.Context, req *filesystem.EditRequest) error {
	p := req.FilePath
	if !filepath.IsAbs(p) {
		p = filepath.Join(b.root, p)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	content := string(data)
	if req.OldString == "" {
		return fmt.Errorf("old_string must not be empty")
	}
	if !req.ReplaceAll {
		count := strings.Count(content, req.OldString)
		if count == 0 {
			return fmt.Errorf("old_string not found in file")
		}
		if count > 1 {
			return fmt.Errorf("old_string appears %d times; set replace_all=true or make it unique", count)
		}
	}
	n := 1
	if req.ReplaceAll {
		n = -1
	}
	newContent := strings.Replace(content, req.OldString, req.NewString, n)
	return os.WriteFile(p, []byte(newContent), 0644)
}

// localShell implements filesystem.Shell

type localShell struct{ cwd string }

func newLocalShell(cwd string) filesystem.Shell {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	return &localShell{cwd: cwd}
}

func (s *localShell) Execute(ctx context.Context, input *filesystem.ExecuteRequest) (*filesystem.ExecuteResponse, error) {
	cmd := exec.CommandContext(ctx, "bash", "-lc", input.Command)
	cmd.Dir = s.cwd
	out, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, err
		}
	}
	return &filesystem.ExecuteResponse{Output: string(out), ExitCode: &exitCode}, nil
}
