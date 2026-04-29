package registry

import (
	"bytes"
	"eino-cli/backend/config"
	"eino-cli/backend/tools"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Registry struct {
	tools map[string]tools.Tool
	mu    sync.RWMutex
}

func New() *Registry {
	items := []tools.Tool{
		{
			Name:        "read",
			Description: "Read a local file",
			Source:      "builtin",
			Capability:  "filesystem",
			Execute:     readFile,
		},
		{
			Name:        "ls",
			Description: "List a directory",
			Source:      "builtin",
			Capability:  "filesystem",
			Execute:     listDir,
		},
		{
			Name:        "shell",
			Description: "Run a shell command",
			Source:      "builtin",
			Capability:  "shell",
			Execute:     runShell,
		},
		{
			Name:        "fetch",
			Description: "Fetch content from a URL (HTTP GET)",
			Source:      "builtin",
			Capability:  "web",
			Execute:     fetchURL,
		},
		{
			Name:        "search",
			Description: "Search the web via Tavily (requires TAVILY_API_KEY)",
			Source:      "builtin",
			Capability:  "web",
			Execute:     searchWeb,
		},
	}

	tools := make(map[string]tools.Tool, len(items))
	for _, tool := range items {
		tools[tool.Name] = tool
	}

	return &Registry{tools: tools}
}

func readFile(args []string, cwd string) (tools.Result, error) {
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

func listDir(args []string, cwd string) (tools.Result, error) {
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

func runShell(args []string, cwd string) (tools.Result, error) {
	if len(args) == 0 {
		return tools.Result{}, fmt.Errorf("shell requires a command")
	}
	command := exec.Command("bash", "-lc", strings.Join(args, " ")) // ignore_security_alert
	command.Dir = cwd
	output, err := command.CombinedOutput()
	if err != nil {
		return tools.Result{Output: string(output)}, err
	}
	return tools.Result{Output: string(output)}, nil
}

func fetchURL(args []string, cwd string) (tools.Result, error) {
	if len(args) == 0 {
		return tools.Result{}, fmt.Errorf("fetch: requires a URL argument")
	}
	rawURL := strings.TrimSpace(args[0])
	if rawURL == "" {
		return tools.Result{}, fmt.Errorf("fetch: URL must not be empty")
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(rawURL)
	if err != nil {
		return tools.Result{}, fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return tools.Result{}, fmt.Errorf("fetch %s: HTTP %d", rawURL, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 100*1024))
	if err != nil {
		return tools.Result{}, fmt.Errorf("fetch %s: read response: %w", rawURL, err)
	}
	return tools.Result{Output: string(body)}, nil
}

func searchWeb(args []string, cwd string) (tools.Result, error) {
	if len(args) == 0 {
		return tools.Result{}, fmt.Errorf("search: requires a query argument")
	}
	query := strings.Join(args, " ")
	apiKey := strings.TrimSpace(os.Getenv("TAVILY_API_KEY"))
	if apiKey == "" {
		return tools.Result{}, fmt.Errorf("search: TAVILY_API_KEY environment variable not set")
	}
	return tavilySearch(query, apiKey)
}

func tavilySearch(query, apiKey string) (tools.Result, error) {
	payload, err := json.Marshal(map[string]any{
		"api_key":        apiKey,
		"query":          query,
		"search_depth":   "basic",
		"include_answer": true,
		"max_results":    5,
	})
	if err != nil {
		return tools.Result{}, fmt.Errorf("marshal search request: %w", err)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post("https://api.tavily.com/search", "application/json", bytes.NewReader(payload))
	if err != nil {
		return tools.Result{}, fmt.Errorf("tavily search: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return tools.Result{}, fmt.Errorf("read tavily response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return tools.Result{}, fmt.Errorf("tavily search HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	type result struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
	}
	type response struct {
		Answer  string   `json:"answer"`
		Results []result `json:"results"`
	}
	var tr response
	if err := json.Unmarshal(body, &tr); err != nil {
		return tools.Result{Output: string(body)}, nil
	}

	var lines []string
	if a := strings.TrimSpace(tr.Answer); a != "" {
		lines = append(lines, "Answer: "+a, "")
	}
	for i, r := range tr.Results {
		lines = append(lines, fmt.Sprintf("[%d] %s", i+1, r.Title))
		lines = append(lines, r.URL)
		if c := strings.TrimSpace(r.Content); c != "" {
			lines = append(lines, c)
		}
		lines = append(lines, "")
	}
	return tools.Result{Output: strings.TrimSpace(strings.Join(lines, "\n"))}, nil
}

func resolvePath(cwd, value string) string {
	if filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(cwd, value)
}

func (r *Registry) GetAvailableTools(_ config.Config, _ string, _ bool) ([]tools.Tool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]tools.Tool, 0, len(r.tools))
	for _, tool := range r.tools {
		// 不包含 Execute 函数到输出中
		out = append(out, tools.Tool{
			Name:        tool.Name,
			Description: tool.Description,
			Source:      tool.Source,
			Capability:  tool.Capability,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (r *Registry) Get(name string) (tools.Tool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tool, ok := r.tools[name]
	if !ok {
		return tools.Tool{}, fmt.Errorf("unknown tool: %s", name)
	}
	return tool, nil
}

func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
