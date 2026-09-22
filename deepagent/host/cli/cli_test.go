package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseRootAndPromptSources(t *testing.T) {
	t.Setenv("SGADK_ROOT", t.TempDir())
	o, err := Parse([]string{"--root", t.TempDir(), "--prompt", "hello", "--session", "s", "--thread", "t"})
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(o.Root) || o.Prompt != "hello" || o.SessionID != "s" || o.ThreadID != "t" {
		t.Fatalf("bad options: %+v", o)
	}
	if _, err := Parse([]string{"--prompt", "hello", "extra"}); err == nil {
		t.Fatal("accepted conflicting prompt sources")
	}
}
func TestCLIConfigDoesNotRequireOrLoadModelCredentials(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("TEST_DEEPAGENT_DSN", "user:password@tcp(localhost:3306)/deepagent")
	if err := os.WriteFile(p, []byte("manager:\n  namespace: sample\n  mysql_dsn: ${TEST_DEEPAGENT_DSN}\n  redis_addr: localhost:6379\nmodels: deliberately-not-a-model-list\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadManagerConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MySQLDSN != "user:password@tcp(localhost:3306)/deepagent" || cfg.RedisAddr != "localhost:6379" {
		t.Fatalf("config not expanded")
	}
}

func TestParseThreadID(t *testing.T) {
	if id, err := parseThreadID("42"); err != nil || id != 42 {
		t.Fatalf("id=%d err=%v", id, err)
	}
	if _, err := parseThreadID("old-string-id"); err == nil {
		t.Fatal("accepted a legacy string thread id")
	}
}
