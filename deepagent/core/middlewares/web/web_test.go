package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
)

func TestReadURLExtractsVisibleText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><style>hidden</style><body>Hello <b>world</b></body></html>`))
	}))
	defer server.Close()

	items, err := New(&WebConfig{EnableFetchURL: true}).Tools(context.Background())
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
	items, err := New(&WebConfig{EnableWebSearch: true, SearchURL: server.URL}).Tools(context.Background())
	if err != nil || len(items) != 1 {
		t.Fatalf("tools = %d, %v", len(items), err)
	}
	output, err := items[0].(tool.InvokableTool).InvokableRun(context.Background(), `{"query":"deep agent"}`)
	if err != nil || !strings.Contains(output, "deep agent") {
		t.Fatalf("web_search = %q, %v", output, err)
	}
}
