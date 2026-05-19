package aio

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"eino-cli/backend/sandbox"
	"eino-cli/backend/sandbox/search"
)

// Sandbox is the per-container HTTP client implementing sandbox.Sandbox.
type Sandbox struct {
	id      string
	baseURL string
	http    *http.Client

	// shellMu serialises exec: the agent-sandbox image keeps one persistent
	// shell session whose state can corrupt under concurrent invocation.
	shellMu sync.Mutex
}

const (
	defaultHTTPTimeout = 600 * time.Second // upstream Python SDK parity
	execTimeoutSeconds = 600
)

func newSandbox(id, baseURL string) *Sandbox {
	return &Sandbox{
		id:      id,
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: defaultHTTPTimeout},
	}
}

// ID returns the sandbox id.
func (s *Sandbox) ID() string { return s.id }

// envelope is the FastAPI response shape every endpoint returns.
type envelope struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// ExecuteCommand runs cmd via POST /v1/shell/exec in sync mode.
func (s *Sandbox) ExecuteCommand(ctx context.Context, cmd string) (string, error) {
	s.shellMu.Lock()
	defer s.shellMu.Unlock()

	body := map[string]any{
		"command":    cmd,
		"async_mode": false,
		"timeout":    execTimeoutSeconds,
	}
	var data struct {
		SessionID string `json:"session_id"`
		Command   string `json:"command"`
		Status    string `json:"status"`
		Output    string `json:"output"`
		ExitCode  *int   `json:"exit_code"`
	}
	if err := s.post(ctx, "/v1/shell/exec", body, &data); err != nil {
		return "", sandbox.NewCommandError(err.Error(), cmd, -1)
	}
	out := data.Output
	if out == "" {
		out = "(no output)"
	}
	if data.ExitCode != nil && *data.ExitCode != 0 {
		out = fmt.Sprintf("%s\nExit Code: %d", out, *data.ExitCode)
	}
	return out, nil
}

// ReadFile reads the full file at path via POST /v1/file/read.
func (s *Sandbox) ReadFile(ctx context.Context, path string) (string, error) {
	var data struct {
		Content string `json:"content"`
		File    string `json:"file"`
	}
	if err := s.post(ctx, "/v1/file/read", map[string]any{"file": path}, &data); err != nil {
		return "", sandbox.NewFileError(err.Error(), path, "read")
	}
	return data.Content, nil
}

// WriteFile writes content via POST /v1/file/write; appendMode uses native append:true.
func (s *Sandbox) WriteFile(ctx context.Context, path, content string, appendMode bool) error {
	body := map[string]any{
		"file":     path,
		"content":  content,
		"encoding": "utf-8",
		"append":   appendMode,
	}
	if err := s.post(ctx, "/v1/file/write", body, nil); err != nil {
		return sandbox.NewFileError(err.Error(), path, "write")
	}
	return nil
}

// UpdateFile writes binary content base64-encoded via POST /v1/file/write.
func (s *Sandbox) UpdateFile(ctx context.Context, path string, content []byte) error {
	body := map[string]any{
		"file":     path,
		"content":  base64.StdEncoding.EncodeToString(content),
		"encoding": "base64",
	}
	if err := s.post(ctx, "/v1/file/write", body, nil); err != nil {
		return sandbox.NewFileError(err.Error(), path, "update")
	}
	return nil
}

// ListDir lists path entries via POST /v1/file/list with native max_depth.
func (s *Sandbox) ListDir(ctx context.Context, path string, maxDepth int) ([]string, error) {
	if maxDepth <= 0 {
		maxDepth = 2
	}
	body := map[string]any{
		"path":        path,
		"recursive":   true,
		"show_hidden": false,
		"max_depth":   maxDepth,
	}
	var data struct {
		Path  string     `json:"path"`
		Files []fileInfo `json:"files"`
	}
	if err := s.post(ctx, "/v1/file/list", body, &data); err != nil {
		return nil, sandbox.NewFileError(err.Error(), path, "list")
	}
	out := make([]string, 0, len(data.Files))
	for _, f := range data.Files {
		entry := f.Path
		if f.IsDirectory && !strings.HasSuffix(entry, "/") {
			entry += "/"
		}
		out = append(out, entry)
	}
	return out, nil
}

type fileInfo struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	IsDirectory bool   `json:"is_directory"`
	Size        *int64 `json:"size"`
	Extension   string `json:"extension"`
}

// Glob matches pattern under path; falls back to /v1/file/list for dirs.
func (s *Sandbox) Glob(ctx context.Context, path, pattern string, opts sandbox.GlobOpts) ([]string, bool, error) {
	maxResults := opts.MaxResults
	if maxResults <= 0 {
		maxResults = 200
	}
	if !opts.IncludeDirs {
		var data struct {
			Path  string   `json:"path"`
			Files []string `json:"files"`
		}
		body := map[string]any{"path": path, "glob": pattern}
		if err := s.post(ctx, "/v1/file/find", body, &data); err != nil {
			return nil, false, sandbox.NewFileError(err.Error(), path, "glob")
		}
		filtered := filterIgnoredPaths(data.Files)
		truncated := len(filtered) > maxResults
		if truncated {
			filtered = filtered[:maxResults]
		}
		return filtered, truncated, nil
	}

	entries, err := s.listPathRecursive(ctx, path)
	if err != nil {
		return nil, false, sandbox.NewFileError(err.Error(), path, "glob")
	}
	root := strings.TrimRight(path, "/")
	if root == "" {
		root = "/"
	}
	prefix := root
	if root != "/" {
		prefix = root + "/"
	}
	var matches []string
	for _, e := range entries {
		if e.Path != root && !strings.HasPrefix(e.Path, prefix) {
			continue
		}
		if isIgnoredPath(e.Path) {
			continue
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(e.Path, root), "/")
		if search.PathMatches(pattern, rel) {
			matches = append(matches, e.Path)
			if len(matches) >= maxResults {
				return matches, true, nil
			}
		}
	}
	return matches, false, nil
}

// Grep searches files for pattern via per-file POST /v1/file/search.
func (s *Sandbox) Grep(ctx context.Context, path, pattern string, opts sandbox.GrepOpts) ([]sandbox.GrepMatch, bool, error) {
	maxResults := opts.MaxResults
	if maxResults <= 0 {
		maxResults = 100
	}
	regex := pattern
	if opts.Literal {
		regex = regexp.QuoteMeta(pattern)
	}
	if !opts.CaseSensitive {
		regex = "(?i)" + regex
	}

	var candidates []string
	if opts.Glob != "" {
		var data struct {
			Files []string `json:"files"`
		}
		body := map[string]any{"path": path, "glob": opts.Glob}
		if err := s.post(ctx, "/v1/file/find", body, &data); err != nil {
			return nil, false, sandbox.NewFileError(err.Error(), path, "grep")
		}
		candidates = data.Files
	} else {
		entries, err := s.listPathRecursive(ctx, path)
		if err != nil {
			return nil, false, sandbox.NewFileError(err.Error(), path, "grep")
		}
		for _, e := range entries {
			if !e.IsDirectory {
				candidates = append(candidates, e.Path)
			}
		}
	}

	var matches []sandbox.GrepMatch
	for _, file := range candidates {
		if isIgnoredPath(file) {
			continue
		}
		body := map[string]any{"file": file, "regex": regex}
		var data struct {
			File        string   `json:"file"`
			Matches     []string `json:"matches"`
			LineNumbers []int    `json:"line_numbers"`
		}
		if err := s.post(ctx, "/v1/file/search", body, &data); err != nil {
			continue
		}
		count := min(len(data.LineNumbers), len(data.Matches))
		for i := range count {
			matches = append(matches, sandbox.GrepMatch{
				Path:       file,
				LineNumber: data.LineNumbers[i],
				Line:       truncateLine(data.Matches[i], 200),
			})
			if len(matches) >= maxResults {
				return matches, true, nil
			}
		}
	}
	return matches, false, nil
}

func (s *Sandbox) listPathRecursive(ctx context.Context, path string) ([]fileInfo, error) {
	body := map[string]any{
		"path":        path,
		"recursive":   true,
		"show_hidden": false,
	}
	var data struct {
		Files []fileInfo `json:"files"`
	}
	if err := s.post(ctx, "/v1/file/list", body, &data); err != nil {
		return nil, err
	}
	return data.Files, nil
}

// post is the single JSON RPC primitive; decodeData=nil discards the response data.
func (s *Sandbox) post(ctx context.Context, path string, body, decodeData any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+path, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("aio: %s %s: HTTP %d: %s", req.Method, path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("aio: %s %s: decode envelope: %w (body=%s)", req.Method, path, err, truncateLine(string(raw), 200))
	}
	if !env.Success {
		return fmt.Errorf("aio: %s %s: %s", req.Method, path, env.Message)
	}
	if decodeData == nil || len(env.Data) == 0 || string(env.Data) == "null" {
		return nil
	}
	return json.Unmarshal(env.Data, decodeData)
}

func truncateLine(line string, maxChars int) string {
	line = strings.TrimRight(line, "\r\n")
	if len(line) <= maxChars {
		return line
	}
	return line[:maxChars-3] + "..."
}

func isIgnoredPath(p string) bool {
	for _, seg := range strings.Split(strings.ReplaceAll(p, "\\", "/"), "/") {
		if seg != "" && search.ShouldIgnoreName(seg) {
			return true
		}
	}
	return false
}

func filterIgnoredPaths(in []string) []string {
	out := make([]string, 0, len(in))
	for _, p := range in {
		if !isIgnoredPath(p) {
			out = append(out, p)
		}
	}
	return out
}
