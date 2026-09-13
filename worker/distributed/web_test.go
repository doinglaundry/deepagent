package distributed

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestWebSearchEscapesQueryAndReadLimitsResponse(t *testing.T) {
	var target string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		target = r.URL.String()
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: io.NopCloser(strings.NewReader("abcdef"))}, nil
	})}
	tools, err := webTools(WebConfig{Enabled: true, SearchURL: "https://search.example/query", MaxBytes: 4, HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	result, err := tools[1].(*webTool).InvokableRun(context.Background(), `{"query":"a & b"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(target, "q=a+%26+b") || !strings.Contains(result, "abcd") || !strings.Contains(result, "truncated") {
		t.Fatalf("target %s result %s", target, result)
	}
}
func TestWebToolRejectsFileScheme(t *testing.T) {
	tools, err := webTools(WebConfig{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tools[0].(*webTool).InvokableRun(context.Background(), `{"url":"file:///etc/passwd"}`); err == nil {
		t.Fatal("file URL accepted")
	}
}
