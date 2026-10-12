package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	agentmodel "eino-cli/deepagent/model"
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

// A documented web.enabled-only configuration must expose usable tools.
func TestWebEnabledUsesDefaultToolsAndPreservesExplicitSelection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("WEB_ENABLED_OK " + r.URL.Query().Get("q")))
	}))
	defer server.Close()
	for _, testCase := range []struct {
		name   string
		config WebConfig
		want   []string
	}{
		{name: "disabled", config: WebConfig{}, want: nil},
		{name: "enabled_read", config: WebConfig{Enabled: true}, want: []string{"read_url"}},
		{name: "enabled_search", config: WebConfig{Enabled: true, SearchURL: server.URL}, want: []string{"read_url", "web_search"}},
		{name: "explicit_read", config: WebConfig{Enabled: true, EnableFetchURL: true, SearchURL: server.URL}, want: []string{"read_url"}},
		{name: "explicit_search", config: WebConfig{Enabled: true, EnableWebSearch: true, SearchURL: server.URL}, want: []string{"web_search"}},
		{name: "select_read_without_master", config: WebConfig{EnableFetchURL: true}, want: []string{"read_url"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			descriptors, err := NewWebTools(ctx, &testCase.config)
			if err != nil {
				t.Fatal(err)
			}
			if len(descriptors) != len(testCase.want) {
				t.Fatalf("registered %d tools, want %v", len(descriptors), testCase.want)
			}
			for index, descriptor := range descriptors {
				info, err := descriptor.Tool.Info(ctx)
				if err != nil || info.Name != testCase.want[index] {
					t.Fatalf("tool %d: info=%+v err=%v", index, info, err)
				}
				arguments := `{"url":"` + server.URL + `"}`
				if info.Name == "web_search" {
					arguments = `{"query":"selection works"}`
				}
				output, err := descriptor.Tool.(tool.InvokableTool).InvokableRun(ctx, arguments)
				if err != nil || !strings.Contains(output, "WEB_ENABLED_OK") {
					t.Fatalf("%s unusable: output=%q err=%v", info.Name, output, err)
				}
			}
		})
	}
}

func TestWebResearchStopsAfterThreeFailuresAcrossBothTools(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: webRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		deadline, ok := request.Context().Deadline()
		if !ok || time.Until(deadline) > 20*time.Second {
			t.Fatal("missing per-request deadline")
		}
		return &http.Response{StatusCode: 429, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("blocked"))}, nil
	})}
	descriptors, err := NewWebTools(context.Background(), &WebConfig{Enabled: true, SearchURL: "https://search.example", HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	state := &agentmodel.RunState{}
	ctx := agentmodel.WithRunState(context.Background(), state)
	for index := range 4 {
		arguments := `{"url":"https://page.example"}`
		descriptor := descriptors[0]
		if index%2 == 1 {
			descriptor = descriptors[1]
			arguments = `{"query":"test"}`
		}
		_, err = descriptor.Tool.(tool.InvokableTool).InvokableRun(ctx, arguments)
		if err == nil {
			t.Fatal("HTTP failure lost")
		}
		if index >= 2 && !strings.Contains(err.Error(), "Summarize") {
			t.Fatalf("missing stop instruction: %v", err)
		}
	}
	if requests != 3 {
		t.Fatalf("continued fetching after failures: %d", requests)
	}
	fresh := agentmodel.WithRunState(context.Background(), &agentmodel.RunState{})
	_, _ = descriptors[0].Tool.(tool.InvokableTool).InvokableRun(fresh, `{"url":"https://page.example"}`)
	if requests != 4 {
		t.Fatal("a previous Run blocked the new Run")
	}
}

func TestWebResearchBudgetSurvivesCheckpointAndCapsRemainingRequest(t *testing.T) {
	state := &agentmodel.RunState{WebSearchDuration: 119 * time.Second}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	restored := &agentmodel.RunState{}
	err = json.Unmarshal(encoded, restored)
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	client := &http.Client{Transport: webRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		deadline, ok := request.Context().Deadline()
		if !ok || time.Until(deadline) > time.Second {
			t.Fatal("remaining search budget was reset")
		}
		return nil, fmt.Errorf("timeout")
	})}
	descriptors, err := NewWebTools(context.Background(), &WebConfig{EnableFetchURL: true, HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	ctx := agentmodel.WithRunState(context.Background(), restored)
	_, _ = descriptors[0].Tool.(tool.InvokableTool).InvokableRun(ctx, `{"url":"https://page.example"}`)
	restored.WebSearchDuration = 2 * time.Minute
	_, err = descriptors[0].Tool.(tool.InvokableTool).InvokableRun(ctx, `{"url":"https://page.example"}`)
	if err == nil || !strings.Contains(err.Error(), "Summarize") || requests != 1 {
		t.Fatalf("budget not enforced: requests=%d err=%v", requests, err)
	}
}

func TestWebResearchDeadlineAlsoStopsReadingTheBody(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
	}))
	defer server.Close()
	descriptors, err := NewWebTools(context.Background(), &WebConfig{EnableFetchURL: true})
	if err != nil {
		t.Fatal(err)
	}
	state := &agentmodel.RunState{WebSearchDuration: 2*time.Minute - 50*time.Millisecond}
	ctx := agentmodel.WithRunState(context.Background(), state)
	arguments := `{"url":"` + server.URL + `"}`
	_, err = descriptors[0].Tool.(tool.InvokableTool).InvokableRun(ctx, arguments)
	if err == nil || state.WebSearchDuration < 2*time.Minute {
		t.Fatalf("body read escaped deadline: %v duration=%s", err, state.WebSearchDuration)
	}
	_, err = descriptors[0].Tool.(tool.InvokableTool).InvokableRun(ctx, arguments)
	if err == nil || requests != 1 {
		t.Fatalf("fetch continued after timeout: %v requests=%d", err, requests)
	}
}

func TestWebResearchSuccessResetsFailuresAndCancellationDoesNotCount(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: webRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
		requests++
		status := http.StatusTooManyRequests
		if requests%3 == 0 {
			status = http.StatusOK
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("response"))}, nil
	})}
	descriptors, err := NewWebTools(context.Background(), &WebConfig{EnableFetchURL: true, HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	state := &agentmodel.RunState{}
	ctx := agentmodel.WithRunState(context.Background(), state)
	webTool := descriptors[0].Tool.(tool.InvokableTool)
	for range 6 {
		_, _ = webTool.InvokableRun(ctx, `{"url":"https://page.example"}`)
	}
	if requests != 6 || state.WebSearchFailures != 0 {
		t.Fatal("successful response did not reset consecutive failures")
	}
	canceledContext, cancel := context.WithCancel(ctx)
	cancel()
	_, err = webTool.InvokableRun(canceledContext, `{"url":"https://page.example"}`)
	if err == nil || requests != 6 || state.WebSearchFailures != 0 {
		t.Fatal("user cancellation counted as a website failure")
	}
}

func TestWebToolRejectsVerificationPagesEvenWithHTTP200(t *testing.T) {
	for _, testCase := range []struct{ name, url, body, challenge string }{
		{"cloudflare", "https://page.example", "security verification", "challenge"},
		{"google traffic", "https://www.google.com/sorry/index", "Our systems have detected unusual traffic from your computer network.", ""},
		{"brave captcha", "https://search.brave.com/search?q=test", "Your request has been flagged as being suspicious and Brave Search decided to schedule a captcha for you.", ""},
		{"google javascript", "https://www.google.com/search?q=test", `<html><body>Please click <a href="/httpservice/retry/enablejs">here</a> if you are not redirected within a few seconds.</body></html>`, ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			client := &http.Client{Transport: webRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/html"}, "Cf-Mitigated": []string{testCase.challenge}}, Body: io.NopCloser(strings.NewReader(testCase.body)), Request: request}, nil
			})}
			descriptors, err := NewWebTools(context.Background(), &WebConfig{EnableFetchURL: true, HTTPClient: client})
			if err != nil {
				t.Fatal(err)
			}
			state := &agentmodel.RunState{}
			ctx := agentmodel.WithRunState(context.Background(), state)
			_, err = descriptors[0].Tool.(tool.InvokableTool).InvokableRun(ctx, `{"url":"`+testCase.url+`"}`)
			if err == nil || !strings.Contains(err.Error(), "browser") || state.WebSearchFailures != 1 {
				t.Fatalf("verification counted as content: %v failures=%d", err, state.WebSearchFailures)
			}
		})
	}
}

func TestWebToolReturnsRedirectDestinationAndResolvedSourceLinks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/start" {
			http.Redirect(writer, request, "/docs/final", http.StatusFound)
			return
		}
		writer.Header().Set("Content-Type", "text/html")
		_, _ = writer.Write([]byte(`<p>An article about captcha implementation.</p><a href="../report">Source report</a><a href="../report">Duplicate</a><a href="javascript:alert(1)">Ignore</a><a href="mailto:contact@example.com">Ignore</a>`))
	}))
	defer server.Close()
	descriptors, err := NewWebTools(context.Background(), &WebConfig{EnableFetchURL: true})
	if err != nil {
		t.Fatal(err)
	}
	output, err := descriptors[0].Tool.(tool.InvokableTool).InvokableRun(context.Background(), `{"url":"`+server.URL+`/start"}`)
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		URL     string
		Content string
		Links   []string
	}
	err = json.Unmarshal([]byte(output), &page)
	if err != nil {
		t.Fatal(err)
	}
	if page.URL != server.URL+"/docs/final" || len(page.Links) != 1 || page.Links[0] != server.URL+"/report" || !strings.Contains(page.Content, "captcha implementation") {
		t.Fatalf("page lost its sources: %s", output)
	}
}
