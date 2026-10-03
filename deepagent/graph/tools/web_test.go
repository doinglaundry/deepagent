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
	maskCalls := 0
	validMaskContext := true
	webConfig := &WebConfig{
		EnableFetchURL: true, EnableWebSearch: true, SearchURL: "https://search.example/query",
		ToolMask: func(maskContext context.Context, toolInfo *schema.ToolInfo) bool {
			maskCalls++
			if maskContext.Value(webMaskContextKey{}) != "present" {
				validMaskContext = false
			}
			return toolInfo.Name == "read_url"
		},
	}
	toolDescriptors, err := NewWebTools(ctx, webConfig)
	if err != nil {
		t.Fatal(err)
	}
	if len(toolDescriptors) != 1 {
		t.Fatalf("tools = %d, want only read_url", len(toolDescriptors))
	}
	toolInfo, err := toolDescriptors[0].Tool.Info(ctx)
	if err != nil || toolInfo.Name != "read_url" {
		t.Fatalf("tool = %v, %v", toolInfo, err)
	}
	if maskCalls != 2 || !validMaskContext {
		t.Fatalf("mask calls = %d, valid context = %v", maskCalls, validMaskContext)
	}
	toolDescriptors, err = NewWebTools(ctx, nil)
	if err != nil || toolDescriptors != nil {
		t.Fatalf("nil config = %v, %v", toolDescriptors, err)
	}
	_, err = NewWebTools(ctx, &WebConfig{EnableFetchURL: true, MaxBytes: -1})
	if err == nil {
		t.Fatal("invalid web config was accepted")
	}
}

func TestWebSearchEscapesQueryAndReadLimitsResponse(t *testing.T) {
	var requestedURL string
	httpClient := &http.Client{Transport: webRoundTripFunc(func(httpRequest *http.Request) (*http.Response, error) {
		requestedURL = httpRequest.URL.String()
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: io.NopCloser(strings.NewReader("abcdef"))}, nil
	})}
	toolDescriptors, err := NewWebTools(context.Background(), &WebConfig{Enabled: true, EnableFetchURL: true, EnableWebSearch: true, SearchURL: "https://search.example/query", MaxBytes: 4, HTTPClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	result, err := toolDescriptors[1].Tool.(tool.InvokableTool).InvokableRun(context.Background(), `{"query":"a & b"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(requestedURL, "q=a+%26+b") || !strings.Contains(result, "abcd") || !strings.Contains(result, "truncated") {
		t.Fatalf("target %s result %s", requestedURL, result)
	}
}
func TestWebToolRejectsFileScheme(t *testing.T) {
	toolDescriptors, err := NewWebTools(context.Background(), &WebConfig{Enabled: true, EnableFetchURL: true, EnableWebSearch: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = toolDescriptors[0].Tool.(tool.InvokableTool).InvokableRun(context.Background(), `{"url":"file:///etc/passwd"}`)
	if err == nil {
		t.Fatal("file URL accepted")
	}
}

func TestWebSearchHeadersStayOnConfiguredOrigin(t *testing.T) {
	for _, destination := range []string{"https://other.example/page", "http://search.example/page"} {
		t.Run(destination, func(t *testing.T) {
			requestCount := 0
			httpClient := &http.Client{Transport: webRoundTripFunc(func(httpRequest *http.Request) (*http.Response, error) {
				requestCount++
				if requestCount == 1 {
					if httpRequest.Header.Get("X-Search-Key") != "secret" {
						t.Fatal("search credential missing")
					}
					return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{destination}}, Body: io.NopCloser(strings.NewReader("")), Request: httpRequest}, nil
				}
				if httpRequest.Header.Get("X-Search-Key") != "" {
					t.Fatal("search credential crossed origin")
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("result")), Request: httpRequest}, nil
			})}
			toolDescriptors, err := NewWebTools(context.Background(), &WebConfig{EnableFetchURL: true, EnableWebSearch: true, SearchURL: "https://search.example/query", Headers: map[string]string{"X-Search-Key": "secret"}, HTTPClient: httpClient})
			if err != nil {
				t.Fatal(err)
			}
			_, err = toolDescriptors[1].Tool.(tool.InvokableTool).InvokableRun(context.Background(), `{"query":"go"}`)
			if err != nil {
				t.Fatal(err)
			}
			if requestCount != 2 {
				t.Fatalf("redirect calls=%d", requestCount)
			}
			// Arbitrary read URLs must not receive the configured search headers.
			_, err = toolDescriptors[0].Tool.(tool.InvokableTool).InvokableRun(context.Background(), `{"url":"https://page.example"}`)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

type webRoundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip webRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestReadURLExtractsVisibleText(t *testing.T) {
	httpServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("Content-Type", "text/html")
		_, _ = responseWriter.Write([]byte(`<html><style>hidden</style><body>Hello <b>world</b></body></html>`))
	}))
	defer httpServer.Close()

	toolDescriptors, err := NewWebTools(context.Background(), &WebConfig{EnableFetchURL: true})
	if err != nil || len(toolDescriptors) != 1 {
		t.Fatalf("tools = %d, %v", len(toolDescriptors), err)
	}
	output, err := toolDescriptors[0].Tool.(tool.InvokableTool).InvokableRun(context.Background(), `{"url":"`+httpServer.URL+`"}`)
	if err != nil || !strings.Contains(output, "Hello world") || strings.Contains(output, "hidden") {
		t.Fatalf("read_url = %q, %v", output, err)
	}
}

func TestSearchUsesConfiguredEndpoint(t *testing.T) {
	httpServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		_, _ = responseWriter.Write([]byte(request.URL.Query().Get("q")))
	}))
	defer httpServer.Close()
	toolDescriptors, err := NewWebTools(context.Background(), &WebConfig{EnableWebSearch: true, SearchURL: httpServer.URL})
	if err != nil || len(toolDescriptors) != 1 {
		t.Fatalf("tools = %d, %v", len(toolDescriptors), err)
	}
	output, err := toolDescriptors[0].Tool.(tool.InvokableTool).InvokableRun(context.Background(), `{"query":"deep agent"}`)
	if err != nil || !strings.Contains(output, "deep agent") {
		t.Fatalf("web_search = %q, %v", output, err)
	}
}
