//go:build aio_smoke

package aio

import (
	"context"
	"os"
	"strings"
	"testing"

	"eino-cli/backend/sandbox"
)

// To run: AIO_SMOKE_URL=http://localhost:8090 go test -tags=aio_smoke -v ./backend/sandbox/aio/...
// Requires a real all-in-one-sandbox container reachable at that URL.

func smokeBox(t *testing.T) *Sandbox {
	t.Helper()
	url := os.Getenv("AIO_SMOKE_URL")
	if url == "" {
		t.Skip("AIO_SMOKE_URL not set")
	}
	return newSandbox("smoke", "smoke", url, nil)
}

func TestSmokeExec(t *testing.T) {
	s := smokeBox(t)
	out, err := s.ExecuteCommand(context.Background(), "echo hello && pwd")
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("exec output missing 'hello': %q", out)
	}
}

func TestSmokeWriteRead(t *testing.T) {
	s := smokeBox(t)
	ctx := context.Background()
	path := "/tmp/eino-smoke.txt"
	if err := s.WriteFile(ctx, path, "line1\nline2\n", false); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := s.ReadFile(ctx, path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != "line1\nline2\n" {
		t.Fatalf("read mismatch: %q", got)
	}
	if err := s.WriteFile(ctx, path, "line3\n", true); err != nil {
		t.Fatalf("append: %v", err)
	}
	got, _ = s.ReadFile(ctx, path)
	if got != "line1\nline2\nline3\n" {
		t.Fatalf("append mismatch: %q", got)
	}
}

func TestSmokeListAndGlob(t *testing.T) {
	s := smokeBox(t)
	ctx := context.Background()
	if _, err := s.ExecuteCommand(ctx, "mkdir -p /tmp/eino-smoke-glob && echo a > /tmp/eino-smoke-glob/a.txt && echo b > /tmp/eino-smoke-glob/b.md"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	entries, err := s.ListDir(ctx, "/tmp/eino-smoke-glob", 2)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) < 2 {
		t.Fatalf("expected >=2 entries, got %v", entries)
	}

	matches, _, err := s.Glob(ctx, "/tmp/eino-smoke-glob", "*.txt", sandbox.GlobOpts{MaxResults: 10})
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) != 1 || !strings.HasSuffix(matches[0], "a.txt") {
		t.Fatalf("glob result mismatch: %v", matches)
	}
}

func TestSmokeGrep(t *testing.T) {
	s := smokeBox(t)
	ctx := context.Background()
	if _, err := s.ExecuteCommand(ctx, "mkdir -p /tmp/eino-smoke-grep && printf 'hello world\\nfoo bar\\nhello again\\n' > /tmp/eino-smoke-grep/a.txt"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	hits, _, err := s.Grep(ctx, "/tmp/eino-smoke-grep", "hello", sandbox.GrepOpts{MaxResults: 10})
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("expected 2 hello matches, got %v", hits)
	}
}
