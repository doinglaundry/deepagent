package tools

import (
	"context"
	"github.com/cloudwego/eino/components/tool"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestWebSearchEscapesQueryAndReadLimitsResponse(t *testing.T) {
	var target string
	client := &http.Client{Transport: webRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		target = r.URL.String()
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: io.NopCloser(strings.NewReader("abcdef"))}, nil
	})}
	tools, err := NewWebTools(&WebConfig{Enabled: true, EnableFetchURL: true, EnableWebSearch: true, SearchURL: "https://search.example/query", MaxBytes: 4, HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	result, err := tools[1].(tool.InvokableTool).InvokableRun(context.Background(), `{"query":"a & b"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(target, "q=a+%26+b") || !strings.Contains(result, "abcd") || !strings.Contains(result, "truncated") {
		t.Fatalf("target %s result %s", target, result)
	}
}
func TestWebToolRejectsFileScheme(t *testing.T) {
	tools, err := NewWebTools(&WebConfig{Enabled: true, EnableFetchURL: true, EnableWebSearch: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tools[0].(tool.InvokableTool).InvokableRun(context.Background(), `{"url":"file:///etc/passwd"}`); err == nil {
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
			items, err := NewWebTools(&WebConfig{EnableFetchURL: true, EnableWebSearch: true, SearchURL: "https://search.example/query", Headers: map[string]string{"X-Search-Key": "secret"}, HTTPClient: client})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = items[1].(tool.InvokableTool).InvokableRun(context.Background(), `{"query":"go"}`); err != nil {
				t.Fatal(err)
			}
			if calls != 2 {
				t.Fatalf("redirect calls=%d", calls)
			}
			// Arbitrary read URLs must not receive the configured search headers.
			if _, err = items[0].(tool.InvokableTool).InvokableRun(context.Background(), `{"url":"https://page.example"}`); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type webRoundTripFunc func(*http.Request) (*http.Response, error)

func (f webRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
