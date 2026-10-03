package tools

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type webMaskContextKey struct{}

func TestWebFactoryAppliesConfiguredMask(t *testing.T) {
	ctx := context.WithValue(context.Background(), webMaskContextKey{}, "present")
	calls := 0
	validContext := true
	config := &WebConfig{
		EnableFetchURL: true, EnableWebSearch: true, SearchURL: "https://search.example/query",
		ToolMask: func(maskContext context.Context, info *schema.ToolInfo) bool {
			calls++
			if maskContext.Value(webMaskContextKey{}) != "present" {
				validContext = false
			}
			return info.Name == "read_url"
		},
	}
	items, err := NewWebTools(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("tools = %d, want only read_url", len(items))
	}
	info, err := items[0].Tool.Info(ctx)
	if err != nil || info.Name != "read_url" {
		t.Fatalf("tool = %v, %v", info, err)
	}
	if calls != 2 || !validContext {
		t.Fatalf("mask calls = %d, valid context = %v", calls, validContext)
	}
	items, err = NewWebTools(ctx, nil)
	if err != nil || items != nil {
		t.Fatalf("nil config = %v, %v", items, err)
	}
	_, err = NewWebTools(ctx, &WebConfig{EnableFetchURL: true, MaxBytes: -1})
	if err == nil {
		t.Fatal("invalid web config was accepted")
	}
}

func TestWebSearchEscapesQueryAndReadLimitsResponse(t *testing.T) {
	var target string
	client := &http.Client{Transport: webRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		target = r.URL.String()
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: io.NopCloser(strings.NewReader("abcdef"))}, nil
	})}
	tools, err := NewWebTools(context.Background(), &WebConfig{Enabled: true, EnableFetchURL: true, EnableWebSearch: true, SearchURL: "https://search.example/query", MaxBytes: 4, HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	result, err := tools[1].Tool.(tool.InvokableTool).InvokableRun(context.Background(), `{"query":"a & b"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(target, "q=a+%26+b") || !strings.Contains(result, "abcd") || !strings.Contains(result, "truncated") {
		t.Fatalf("target %s result %s", target, result)
	}
}
func TestWebToolRejectsFileScheme(t *testing.T) {
	tools, err := NewWebTools(context.Background(), &WebConfig{Enabled: true, EnableFetchURL: true, EnableWebSearch: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = tools[0].Tool.(tool.InvokableTool).InvokableRun(context.Background(), `{"url":"file:///etc/passwd"}`)
	if err == nil {
		t.Fatal("file URL accepted")
	}
}

func TestWebSearchHeadersStayOnConfiguredOrigin(t *testing.T) {
	for _, destination := range []string{"https://other.example/page", "http://search.example/page"} {
		t.Run(destination, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: webRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					if r.Header.Get("X-Search-Key") != "secret" {
						t.Fatal("search credential missing")
					}
					return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{destination}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
				}
				if r.Header.Get("X-Search-Key") != "" {
					t.Fatal("search credential crossed origin")
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("result")), Request: r}, nil
			})}
			items, err := NewWebTools(context.Background(), &WebConfig{EnableFetchURL: true, EnableWebSearch: true, SearchURL: "https://search.example/query", Headers: map[string]string{"X-Search-Key": "secret"}, HTTPClient: client})
			if err != nil {
				t.Fatal(err)
			}
			_, err = items[1].Tool.(tool.InvokableTool).InvokableRun(context.Background(), `{"query":"go"}`)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 2 {
				t.Fatalf("redirect calls=%d", calls)
			}
			// Arbitrary read URLs must not receive the configured search headers.
			_, err = items[0].Tool.(tool.InvokableTool).InvokableRun(context.Background(), `{"url":"https://page.example"}`)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

type webRoundTripFunc func(*http.Request) (*http.Response, error)

func (f webRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestReadURLExtractsVisibleText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><style>hidden</style><body>Hello <b>world</b></body></html>`))
	}))
	defer server.Close()

	items, err := NewWebTools(context.Background(), &WebConfig{EnableFetchURL: true})
	if err != nil || len(items) != 1 {
		t.Fatalf("tools = %d, %v", len(items), err)
	}
	output, err := items[0].Tool.(tool.InvokableTool).InvokableRun(context.Background(), `{"url":"`+server.URL+`"}`)
	if err != nil || !strings.Contains(output, "Hello world") || strings.Contains(output, "hidden") {
		t.Fatalf("read_url = %q, %v", output, err)
	}
}

func TestSearchUsesConfiguredEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = w.Write([]byte(req.URL.Query().Get("q")))
	}))
	defer server.Close()
	items, err := NewWebTools(context.Background(), &WebConfig{EnableWebSearch: true, SearchURL: server.URL})
	if err != nil || len(items) != 1 {
		t.Fatalf("tools = %d, %v", len(items), err)
	}
	output, err := items[0].Tool.(tool.InvokableTool).InvokableRun(context.Background(), `{"query":"deep agent"}`)
	if err != nil || !strings.Contains(output, "deep agent") {
		t.Fatalf("web_search = %q, %v", output, err)
	}
}
