package middlewares

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
)

// captureSlog redirects slog default output to buf at Debug level for the
// lifetime of t. Restores the original handler when the test ends so
// neighbouring tests aren't poisoned.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	orig := slog.Default()
	t.Cleanup(func() { slog.SetDefault(orig) })

	buf := &bytes.Buffer{}
	h := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	slog.SetDefault(slog.New(h))
	return buf
}

func okEndpoint(want string) adk.InvokableToolCallEndpoint {
	return func(ctx context.Context, args string, opts ...tool.Option) (string, error) {
		return want, nil
	}
}

func errEndpoint(err error) adk.InvokableToolCallEndpoint {
	return func(ctx context.Context, args string, opts ...tool.Option) (string, error) {
		return "", err
	}
}

func TestToolCallObservability_DisabledIsPassthrough(t *testing.T) {
	buf := captureSlog(t)

	mw := NewToolCallObservability(false)
	wrapped, err := mw.WrapInvokableToolCall(context.Background(), okEndpoint("hello"), &adk.ToolContext{Name: "ls"})
	if err != nil {
		t.Fatalf("WrapInvokableToolCall: %v", err)
	}
	got, err := wrapped(context.Background(), `{"path":"."}`)
	if err != nil || got != "hello" {
		t.Fatalf("disabled passthrough: got %q err=%v", got, err)
	}
	if buf.Len() != 0 {
		t.Fatalf("disabled middleware should not log; got %q", buf.String())
	}
}

func TestToolCallObservability_EnabledLogsExit(t *testing.T) {
	buf := captureSlog(t)

	mw := NewToolCallObservability(true)
	wrapped, _ := mw.WrapInvokableToolCall(context.Background(), okEndpoint("hello"), &adk.ToolContext{Name: "ls"})
	got, err := wrapped(context.Background(), `{"path":"."}`)
	if err != nil || got != "hello" {
		t.Fatalf("enabled passthrough: got %q err=%v", got, err)
	}
	logged := buf.String()
	if !strings.Contains(logged, `msg=tool.exit`) {
		t.Fatalf("expected tool.exit record, got: %s", logged)
	}
	if !strings.Contains(logged, `name=ls`) {
		t.Fatalf("expected name=ls in record, got: %s", logged)
	}
	if !strings.Contains(logged, `out_size=5`) {
		t.Fatalf("expected out_size=5 in record, got: %s", logged)
	}
	if strings.Contains(logged, "hello") {
		t.Fatalf("result CONTENT must never appear in logs (privacy invariant): %s", logged)
	}
}

func TestToolCallObservability_EnabledLogsError(t *testing.T) {
	buf := captureSlog(t)

	mw := NewToolCallObservability(true)
	wrapped, _ := mw.WrapInvokableToolCall(context.Background(), errEndpoint(errors.New("boom")), &adk.ToolContext{Name: "execute"})
	out, err := wrapped(context.Background(), `{"command":"false"}`)
	if err == nil || out != "" {
		t.Fatalf("error should propagate untouched; got out=%q err=%v", out, err)
	}
	logged := buf.String()
	if !strings.Contains(logged, `msg=tool.error`) {
		t.Fatalf("expected tool.error record, got: %s", logged)
	}
	if !strings.Contains(logged, `name=execute`) {
		t.Fatalf("expected name=execute in record, got: %s", logged)
	}
	if !strings.Contains(logged, "boom") {
		t.Fatalf("expected err=boom in record, got: %s", logged)
	}
}
