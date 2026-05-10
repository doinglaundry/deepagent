package middlewares

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// stubFetcher is a minimal ImageFetcher used to drive the middleware in tests.
type stubFetcher struct {
	data    []byte
	mime    string
	err     error
	calls   int
	gotPath string
}

func (s *stubFetcher) ReadImage(_ context.Context, path string) ([]byte, string, error) {
	s.calls++
	s.gotPath = path
	if s.err != nil {
		return nil, "", s.err
	}
	return s.data, s.mime, nil
}

// makeStateWithToolCall builds the assistant + tool-result state shape that
// eino's afterToolCalls node hands to AfterToolCallsRewriteState.
func makeStateWithToolCall(callID, name, args, toolResult string) (*adk.ChatModelAgentState, *adk.ToolCallsContext) {
	state := &adk.ChatModelAgentState{
		Messages: []*schema.Message{
			schema.UserMessage("look at this"),
			{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{
					{ID: callID, Function: schema.FunctionCall{Name: name, Arguments: args}},
				},
			},
			{Role: schema.Tool, ToolCallID: callID, ToolName: name, Content: toolResult},
		},
	}
	toolCallsCtx := &adk.ToolCallsContext{
		ToolCalls: []adk.ToolContext{{Name: name, CallID: callID}},
	}
	return state, toolCallsCtx
}

func TestViewImage_AppendsMultimodalUserMessage(t *testing.T) {
	fetcher := &stubFetcher{data: []byte{0x89, 'P', 'N', 'G'}, mime: "image/png"}
	mw := NewViewImage(fetcher)

	state, toolCallsCtx := makeStateWithToolCall("c1", "view_image", `{"path":"/tmp/x.png"}`, "raw bytes")
	_, out, err := mw.AfterToolCallsRewriteState(context.Background(), state, toolCallsCtx)
	if err != nil {
		t.Fatalf("AfterToolCallsRewriteState: %v", err)
	}
	if fetcher.calls != 1 || fetcher.gotPath != "/tmp/x.png" {
		t.Fatalf("fetcher unexpected: calls=%d path=%q", fetcher.calls, fetcher.gotPath)
	}

	if len(out.Messages) != 4 {
		t.Fatalf("expected 4 msgs after rewrite (got %d)", len(out.Messages))
	}
	tool := out.Messages[2]
	if !strings.Contains(tool.Content, "attached as next user message") {
		t.Errorf("tool content should be rewritten to placeholder, got %q", tool.Content)
	}
	usr := out.Messages[3]
	if usr.Role != schema.User {
		t.Fatalf("expected appended User message, got role=%s", usr.Role)
	}
	if len(usr.UserInputMultiContent) < 2 {
		t.Fatalf("expected ≥2 input parts (text + image), got %d", len(usr.UserInputMultiContent))
	}
	imgPart := usr.UserInputMultiContent[1]
	if imgPart.Type != schema.ChatMessagePartTypeImageURL || imgPart.Image == nil {
		t.Fatalf("expected image part second, got %+v", imgPart)
	}
	if imgPart.Image.MIMEType != "image/png" {
		t.Errorf("MIMEType = %q, want image/png", imgPart.Image.MIMEType)
	}
	if imgPart.Image.Base64Data == nil || *imgPart.Image.Base64Data == "" {
		t.Error("Base64Data should be populated")
	}
}

func TestViewImage_FetcherErrorIsSoftSkip(t *testing.T) {
	mw := NewViewImage(&stubFetcher{err: errors.New("boom")})
	state, toolCallsCtx := makeStateWithToolCall("c1", "view_image", `{"path":"x.png"}`, "original tool result")
	_, out, err := mw.AfterToolCallsRewriteState(context.Background(), state, toolCallsCtx)
	if err != nil {
		t.Fatalf("AfterToolCallsRewriteState: %v", err)
	}
	if len(out.Messages) != 3 {
		t.Fatalf("expected no extra messages on fetch error, got %d", len(out.Messages))
	}
	if out.Messages[2].Content != "original tool result" {
		t.Errorf("tool content should not be rewritten on error, got %q", out.Messages[2].Content)
	}
}

func TestViewImage_NoFetcher_NoOp(t *testing.T) {
	mw := NewViewImage(nil)
	state, toolCallsCtx := makeStateWithToolCall("c1", "view_image", `{"path":"x.png"}`, "tool result")
	_, out, err := mw.AfterToolCallsRewriteState(context.Background(), state, toolCallsCtx)
	if err != nil {
		t.Fatalf("AfterToolCallsRewriteState: %v", err)
	}
	if len(out.Messages) != 3 {
		t.Errorf("nil fetcher should not append messages, got %d", len(out.Messages))
	}
}

func TestViewImage_OtherToolsBypass(t *testing.T) {
	fetcher := &stubFetcher{data: []byte("img"), mime: "image/png"}
	mw := NewViewImage(fetcher)
	state, toolCallsCtx := makeStateWithToolCall("c1", "filesystem.read", `{"path":"x.txt"}`, "file content")
	_, out, err := mw.AfterToolCallsRewriteState(context.Background(), state, toolCallsCtx)
	if err != nil {
		t.Fatalf("AfterToolCallsRewriteState: %v", err)
	}
	if fetcher.calls != 0 {
		t.Errorf("fetcher should NOT be called for non-view_image tool, got %d", fetcher.calls)
	}
	if len(out.Messages) != 3 {
		t.Errorf("no rewrite expected, got %d msgs", len(out.Messages))
	}
}

func TestViewImage_MaxBytesExceededIsSoftSkip(t *testing.T) {
	mw := NewViewImage(&stubFetcher{data: []byte("aaaaaa"), mime: "image/png"})
	mw.MaxBytes = 1
	state, toolCallsCtx := makeStateWithToolCall("c1", "view_image", `{"path":"big.png"}`, "tool result")
	_, out, err := mw.AfterToolCallsRewriteState(context.Background(), state, toolCallsCtx)
	if err != nil {
		t.Fatalf("AfterToolCallsRewriteState: %v", err)
	}
	if len(out.Messages) != 3 {
		t.Errorf("expected no User message when MaxBytes exceeded, got %d msgs", len(out.Messages))
	}
}
