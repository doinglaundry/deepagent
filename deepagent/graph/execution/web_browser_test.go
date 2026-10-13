package execution

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"eino-cli/deepagent/graph/computer"
	"eino-cli/deepagent/graph/tools"
	agentmodel "eino-cli/deepagent/model"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func TestWebBrowserVerificationResumesSamePageWithoutRepeatingHTTP(t *testing.T) {
	for _, scenario := range []string{"verified", "still blocked", "canceled", "replaced browser", "different origin"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("DEEPAGENT_BROWSER_HEADLESS", "1")
			var httpReads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				if strings.HasPrefix(r.UserAgent(), "Go-http-client") {
					httpReads.Add(1)
					w.Header().Set("cf-mitigated", "challenge")
					w.WriteHeader(http.StatusForbidden)
				}
				if r.URL.Path == "/verify" {
					http.SetCookie(w, &http.Cookie{Name: "fixture_verified", Value: "yes", Path: "/"})
					http.Redirect(w, r, "/article", http.StatusFound)
					return
				}
				cookie, err := r.Cookie("fixture_verified")
				if err == nil && cookie.Value == "yes" {
					fmt.Fprint(w, `<article>VERIFIED_BROWSER_CONTENT</article><a href="/source">Source</a><a href="http://">Broken link</a><a href="https://www.bing.com/ck/a?u=a1aHR0cHM6Ly9nby5kZXYvZG9jLw">Go source</a>`)
					return
				}
				fmt.Fprint(w, `<form id="challenge-form"><p>Verify you are human</p><a href="/verify">Complete synthetic verification</a></form>`)
			}))
			defer server.Close()
			ctx := context.Background()
			browser, err := computer.NewBrowser(ctx, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { browser.Close(ctx) }()
			chatModel := &sequenceModel{responses: [][]*schema.Message{
				{schema.AssistantMessage("", []schema.ToolCall{{ID: "read", Function: schema.FunctionCall{Name: "read_url", Arguments: `{"url":"` + server.URL + `/article"}`}}})},
				{schema.AssistantMessage("done", nil)},
			}}
			config := Config{Model: chatModel, RunID: "browser-run", CheckpointStore: &checkpointMemory{}, WebConfig: &tools.WebConfig{
				EnableFetchURL: true,
				GetBrowser:     func(context.Context) (*computer.Browser, error) { return browser, nil },
			}}
			graph, err := New(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			_, err = graph.Invoke(ctx, []agentmodel.RunInput{{Message: agentmodel.NewUserMessage("read")}}, RunOptions{CheckpointID: "browser-checkpoint"})
			info, interrupted := compose.ExtractInterruptInfo(err)
			if !interrupted || len(info.InterruptContexts) != 1 {
				t.Fatalf("expected browser verification interrupt: %v", err)
			}
			question, ok := info.InterruptContexts[0].Info.(*agentmodel.FollowUpInfo)
			if !ok || question.BrowserURL != server.URL+"/article" {
				t.Fatalf("verification question=%+v", question)
			}
			if graph.runState.WebSearchFailures != 0 {
				t.Fatal("human verification was counted as a failed website request")
			}
			config.Conversation = graph.conversation
			graph.Close(ctx)
			err = nil
			answer := "continue"
			switch scenario {
			case "verified":
				// 本机受控页面模拟用户完成验证，不求解真实网站验证码。
				_, err = browser.PerformAction(ctx, "open", agentmodel.ComputerAction{URL: server.URL + "/verify"})
			case "canceled":
				answer = "cancel"
			case "replaced browser":
				browser.Close(ctx)
				browser, err = computer.NewBrowser(ctx, t.TempDir())
			case "different origin":
				other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "UNAUTHORIZED_PAGE_CONTENT") }))
				defer other.Close()
				_, err = browser.PerformAction(ctx, "open", agentmodel.ComputerAction{URL: other.URL})
			}
			if err != nil {
				t.Fatal(err)
			}
			resumed, err := New(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			defer resumed.Close(ctx)
			_, err = resumed.Invoke(ctx, nil, RunOptions{CheckpointID: "browser-checkpoint", ResumeData: map[string]any{
				info.InterruptContexts[0].ID: &agentmodel.FollowUpInfo{UserAnswer: answer},
			}})
			if scenario == "still blocked" {
				_, interrupted = compose.ExtractInterruptInfo(err)
				if !interrupted {
					t.Fatalf("verification was not completed but resume succeeded: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				var toolContent string
				for _, message := range resumed.conversation.GetHistory(ctx) {
					if message.Role == schema.Tool {
						toolContent += message.Content
					}
				}
				expected := map[string]string{"verified": "VERIFIED_BROWSER_CONTENT", "canceled": "canceled", "replaced browser": "session expired", "different origin": "origin changed"}[scenario]
				if !strings.Contains(toolContent, expected) {
					t.Fatalf("expected %q in tool output: %s", expected, toolContent)
				}
				if scenario == "verified" && (!strings.Contains(toolContent, "https://go.dev/doc/") || !strings.Contains(toolContent, `"human_verification_completed":true`)) {
					t.Fatal("browser results retained a Bing tracking URL instead of the source URL")
				}
				if strings.Contains(toolContent, "UNAUTHORIZED_PAGE_CONTENT") {
					t.Fatal("read content from a different origin")
				}
			}
			if httpReads.Load() != 1 {
				t.Fatalf("HTTP was retried after human verification: %d", httpReads.Load())
			}
		})
	}
}
