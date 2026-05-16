package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"

	"eino-cli/backend/config"
)

// runWebSearch builds the tool against a stub Bocha server and returns
// the rendered string. cfg.BaseURL is rewired to the test server so we
// never hit api.bochaai.com from CI.
func runWebSearch(t *testing.T, srv *httptest.Server, override func(c *config.WebSearch), query string, max int) (string, error) {
	t.Helper()
	cfg := &config.Config{
		WebSearch: config.WebSearch{
			Enabled:    true,
			Provider:   "bocha",
			APIKey:     "test-key",
			MaxResults: 5,
		},
	}
	if srv != nil {
		cfg.WebSearch.BaseURL = srv.URL
	}
	if override != nil {
		override(&cfg.WebSearch)
	}
	bt, err := GetWebSearchTool(cfg)
	if err != nil {
		t.Fatalf("GetWebSearchTool: %v", err)
	}
	it, ok := bt.(tool.InvokableTool)
	if !ok {
		t.Fatalf("web_search is not InvokableTool")
	}
	args := map[string]any{"query": query}
	if max > 0 {
		args["max_results"] = max
	}
	raw, _ := json.Marshal(args)
	return it.InvokableRun(context.Background(), string(raw))
}

func TestWebSearch_Disabled(t *testing.T) {
	cfg := &config.Config{WebSearch: config.WebSearch{Enabled: false}}
	bt, err := GetWebSearchTool(cfg)
	if err != nil {
		t.Fatalf("GetWebSearchTool: %v", err)
	}
	it := bt.(tool.InvokableTool)
	_, err = it.InvokableRun(context.Background(), `{"query":"hello"}`)
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled web_search should error with 'disabled', got %v", err)
	}
}

func TestWebSearch_EmptyQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("backend should not be called for empty query")
	}))
	defer srv.Close()
	_, err := runWebSearch(t, srv, nil, "   ", 0)
	if err == nil || !strings.Contains(err.Error(), "query") {
		t.Fatalf("empty query should error, got %v", err)
	}
}

func TestWebSearch_Success(t *testing.T) {
	var captured struct {
		auth string
		body []byte
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.auth = r.Header.Get("Authorization")
		captured.body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"data": {
				"webPages": {
					"value": [
						{"name":"Title A","url":"https://a.example","snippet":"snippet a"},
						{"name":"Title B","url":"https://b.example","snippet":"snippet b"}
					]
				}
			}
		}`)
	}))
	defer srv.Close()

	out, err := runWebSearch(t, srv, nil, "weather in beijing", 0)
	if err != nil {
		t.Fatalf("web_search failed: %v", err)
	}
	if !strings.Contains(out, "Title A") || !strings.Contains(out, "https://b.example") {
		t.Fatalf("rendered output missing expected hits:\n%s", out)
	}
	if captured.auth != "Bearer test-key" {
		t.Fatalf("auth header: got %q want %q", captured.auth, "Bearer test-key")
	}
	var sent map[string]any
	if err := json.Unmarshal(captured.body, &sent); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if sent["query"] != "weather in beijing" {
		t.Fatalf("query field: got %v", sent["query"])
	}
	// max_results unset → falls back to cfg.MaxResults (5).
	if got, _ := sent["count"].(float64); got != 5 {
		t.Fatalf("count: got %v want 5", sent["count"])
	}
}

func TestWebSearch_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	_, err := runWebSearch(t, srv, nil, "hello", 0)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("404 should surface, got %v", err)
	}
}

func TestWebSearch_NoResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"webPages":{"value":[]}}}`)
	}))
	defer srv.Close()
	out, err := runWebSearch(t, srv, nil, "hello", 0)
	if err != nil {
		t.Fatalf("no-results case must not error, got %v", err)
	}
	if !strings.Contains(out, "No web results") {
		t.Fatalf("no-results body: %q", out)
	}
}

func TestWebSearch_MissingKey(t *testing.T) {
	_, err := runWebSearch(t, nil, func(c *config.WebSearch) { c.APIKey = "" }, "hello", 0)
	if err == nil || !strings.Contains(err.Error(), "api key") {
		t.Fatalf("missing key should error, got %v", err)
	}
}
