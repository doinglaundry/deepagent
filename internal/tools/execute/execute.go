package execute

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"eino-cli/internal/tools"
)

type Executor struct{}

func New() *Executor {
	return &Executor{}
}

func (e *Executor) Execute(tool tools.Tool, args []string, cwd string) (tools.Result, error) {
	switch tool.Name {
	case "read":
		return e.readFile(args, cwd)
	case "ls":
		return e.listDir(args, cwd)
	case "shell":
		return e.runShell(args, cwd)
	case "fetch":
		return e.fetchURL(args)
	case "search":
		return e.searchWeb(args)
	default:
		return tools.Result{}, fmt.Errorf("unsupported tool: %s", tool.Name)
	}
}

func (e *Executor) readFile(args []string, cwd string) (tools.Result, error) {
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

func (e *Executor) listDir(args []string, cwd string) (tools.Result, error) {
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

func (e *Executor) runShell(args []string, cwd string) (tools.Result, error) {
	if len(args) == 0 {
		return tools.Result{}, fmt.Errorf("shell requires a command")
	}
	command := exec.Command("bash", "-lc", strings.Join(args, " "))
	command.Dir = cwd
	output, err := command.CombinedOutput()
	if err != nil {
		return tools.Result{Output: string(output)}, err
	}
	return tools.Result{Output: string(output)}, nil
}

// fetchURL performs an HTTP GET and returns the response body (capped at 100 KB).
func (e *Executor) fetchURL(args []string) (tools.Result, error) {
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

// searchWeb runs a Tavily web search. Requires TAVILY_API_KEY to be set.
func (e *Executor) searchWeb(args []string) (tools.Result, error) {
	if len(args) == 0 {
		return tools.Result{}, fmt.Errorf("search: requires a query argument")
	}
	query := strings.Join(args, " ")
	apiKey := strings.TrimSpace(os.Getenv("TAVILY_API_KEY"))
	if apiKey == "" {
		return tools.Result{}, fmt.Errorf("search: TAVILY_API_KEY environment variable not set")
	}
	return e.tavilySearch(query, apiKey)
}

func (e *Executor) tavilySearch(query, apiKey string) (tools.Result, error) {
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
