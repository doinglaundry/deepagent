package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"eino-cli/backend/config"
)

const (
	webSearchToolName    = "web_search"
	webSearchDefaultMax  = 5
	webSearchDefaultTimeout = 30 * time.Second
	bochaDefaultEndpoint = "https://api.bochaai.com/v1/web-search"
)

const webSearchToolDesc = `Search the live web for fresh information. Use when answering needs facts beyond the model's training cutoff (current weather, latest news, today's prices, recent releases). Returns titled snippets — cite or summarise them; do NOT pretend you searched when you did not.`

type webSearchArgs struct {
	Query      string `json:"query"                 jsonschema:"required,description=Natural-language search query"`
	MaxResults int    `json:"max_results,omitempty" jsonschema:"description=Max results to return; defaults to provider config (commonly 5)"`
}

// GetWebSearchTool returns the "web_search" function tool. Backed by Bocha
// only today; switch on cfg.WebSearch.Provider when more backends land.
func GetWebSearchTool(cfg *config.Config) (tool.BaseTool, error) {
	wsCfg := cfg.WebSearch
	return utils.InferTool(webSearchToolName, webSearchToolDesc,
		func(ctx context.Context, in webSearchArgs) (string, error) {
			if !wsCfg.Enabled {
				return "", fmt.Errorf("web_search disabled in yaml/config.yaml")
			}
			if strings.TrimSpace(in.Query) == "" {
				return "", fmt.Errorf("web_search query must not be empty")
			}
			max := in.MaxResults
			if max <= 0 {
				max = wsCfg.MaxResults
			}
			if max <= 0 {
				max = webSearchDefaultMax
			}
			return runBochaSearch(ctx, wsCfg, in.Query, max)
		})
}

// runBochaSearch posts to Bocha's /v1/web-search and renders the hits as
// a numbered markdown list. Provider switching (e.g. Tavily, custom)
// would land here as a small switch when the second backend arrives.
func runBochaSearch(ctx context.Context, cfg config.WebSearch, query string, max int) (string, error) {
	endpoint := strings.TrimSpace(cfg.BaseURL)
	if endpoint == "" {
		endpoint = bochaDefaultEndpoint
	}
	apiKey := resolveWebSearchAPIKey(cfg)
	if apiKey == "" {
		return "", fmt.Errorf("web_search api key missing (set web_search.api_key or web_search.api_key_env)")
	}
	body, err := json.Marshal(map[string]any{
		"query": query,
		"count": max,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := webSearchHTTPClient(cfg).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("web_search backend returned %d", resp.StatusCode)
	}

	var parsed struct {
		Data struct {
			WebPages struct {
				Value []struct {
					Name    string `json:"name"`
					URL     string `json:"url"`
					Snippet string `json:"snippet"`
				} `json:"value"`
			} `json:"webPages"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", err
	}
	results := parsed.Data.WebPages.Value
	if len(results) == 0 {
		return "No web results found for query.", nil
	}
	var buf bytes.Buffer
	for i, r := range results {
		fmt.Fprintf(&buf, "%d. [%s](%s)\n   %s\n", i+1, r.Name, r.URL, r.Snippet)
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}

// resolveWebSearchAPIKey mirrors normalizeModels precedence: api_key_env
// (if set) → literal api_key → "" (caller errors out).
func resolveWebSearchAPIKey(cfg config.WebSearch) string {
	if env := strings.TrimSpace(cfg.APIKeyEnv); env != "" {
		return os.Getenv(env)
	}
	return strings.TrimSpace(cfg.APIKey)
}

func webSearchHTTPClient(cfg config.WebSearch) *http.Client {
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = webSearchDefaultTimeout
	}
	return &http.Client{Timeout: timeout}
}
