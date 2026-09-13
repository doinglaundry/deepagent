package cli

import (
	"context"
	"eino-cli/manager"
	"eino-cli/manager/api"
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
	if cfg.Namespace != "sample" || cfg.MySQLDSN != "user:password@tcp(localhost:3306)/deepagent" {
		t.Fatalf("config not expanded")
	}
}

func TestAttachedPlanFlagPersistsWithoutImplicitlyDisabling(t *testing.T) {
	ctx := context.Background()
	m := manager.NewMemory("plan-test")
	thread, err := m.CreateThread(ctx, api.CreateThreadRequest{SessionID: "s", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	options := Options{ThreadID: thread.ID, Plan: true}
	if err := attachThread(ctx, m, &options); err != nil {
		t.Fatal(err)
	}
	options.Plan = false
	if err := attachThread(ctx, m, &options); err != nil {
		t.Fatal(err)
	}
	saved, err := m.GetThread(ctx, thread.ID)
	if err != nil || !saved.PlanMode || options.SessionID != "s" {
		t.Fatalf("attachment lost plan/session: %+v err=%v", saved, err)
	}
}
