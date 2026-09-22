package worker

import (
	"os"
	"path/filepath"
	"testing"

	"eino-cli/deepagent/core/modelhub"
)

func TestLoadConfigExpandsEnvironmentAndValidatesModel(t *testing.T) {
	t.Setenv("TEST_MYSQL_DSN", "user:pass@tcp(localhost:3306)/deepagent")
	path := filepath.Join(t.TempDir(), "worker.yaml")
	err := os.WriteFile(path, []byte(`
manager:
  mysql_dsn: ${TEST_MYSQL_DSN}
  redis_addr: localhost:6379
models:
  - name: primary
    provider: openai
    model: test-model
    api_key: test-key
default_model: primary
`), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Manager.MySQLDSN != os.Getenv("TEST_MYSQL_DSN") || cfg.DefaultModel != "primary" {
		t.Fatalf("config=%+v", cfg)
	}
}

func TestConfigRejectsUnknownDefaultModel(t *testing.T) {
	err := (Config{Manager: ManagerConfig{MySQLDSN: "dsn", RedisAddr: "redis"}, DefaultModel: "missing"}).Validate()
	if err == nil {
		t.Fatal("expected invalid default model")
	}
}

func TestConfigRequiresMemoryDirectoryWhenEnabled(t *testing.T) {
	err := (Config{
		Manager: ManagerConfig{MySQLDSN: "dsn", RedisAddr: "redis"},
		Models:  []modelhub.Config{{Name: "primary"}}, DefaultModel: "primary",
		MemoryEnabled: true,
	}).Validate()
	if err == nil {
		t.Fatal("expected missing memory_dir error")
	}
}
