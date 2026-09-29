package middleware

import (
	"context"
	"eino-cli/deepagent/core/tools"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type webMaskContextKey struct{}

func TestReadURLExtractsVisibleText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><style>hidden</style><body>Hello <b>world</b></body></html>`))
	}))
	defer server.Close()

	items, err := NewWeb(&tools.WebConfig{EnableFetchURL: true}).Tools(context.Background())
	if err != nil || len(items) != 1 {
		t.Fatalf("tools = %d, %v", len(items), err)
	}
	output, err := items[0].(tool.InvokableTool).InvokableRun(context.Background(), `{"url":"`+server.URL+`"}`)
	if err != nil || !strings.Contains(output, "Hello world") || strings.Contains(output, "hidden") {
		t.Fatalf("read_url = %q, %v", output, err)
	}
}

func TestSearchUsesConfiguredEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = w.Write([]byte(req.URL.Query().Get("q")))
	}))
	defer server.Close()
	items, err := NewWeb(&tools.WebConfig{EnableWebSearch: true, SearchURL: server.URL}).Tools(context.Background())
	if err != nil || len(items) != 1 {
		t.Fatalf("tools = %d, %v", len(items), err)
	}
	output, err := items[0].(tool.InvokableTool).InvokableRun(context.Background(), `{"query":"deep agent"}`)
	if err != nil || !strings.Contains(output, "deep agent") {
		t.Fatalf("web_search = %q, %v", output, err)
	}
}

func TestWebToolsApplyConfiguredMask(t *testing.T) {
	ctx := context.WithValue(context.Background(), webMaskContextKey{}, "present")
	calls := 0
	validContext := true
	config := &tools.WebConfig{
		EnableFetchURL:  true,
		EnableWebSearch: true,
		SearchURL:       "https://search.example/query",
		ToolMask: func(maskContext context.Context, info *schema.ToolInfo) bool {
			calls++
			if maskContext.Value(webMaskContextKey{}) != "present" {
				validContext = false
			}
			return info.Name == "read_url"
		},
	}
	items, err := NewWeb(config).Tools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("tools = %d, want 1", len(items))
	}
	info, err := items[0].Info(ctx)
	if err != nil || info.Name != "read_url" {
		t.Fatalf("tool = %v, %v", info, err)
	}
	if calls != 2 || !validContext {
		t.Fatalf("mask calls = %d, valid context = %v", calls, validContext)
	}

	var nilWeb *Web
	items, err = nilWeb.Tools(ctx)
	if err != nil || items != nil {
		t.Fatalf("nil middleware = %v, %v", items, err)
	}
	items, err = NewWeb(nil).Tools(ctx)
	if err != nil || items != nil {
		t.Fatalf("nil config = %v, %v", items, err)
	}
	_, err = NewWeb(&tools.WebConfig{EnableFetchURL: true, MaxBytes: -1}).Tools(ctx)
	if err == nil {
		t.Fatal("invalid web config was accepted")
	}
}
