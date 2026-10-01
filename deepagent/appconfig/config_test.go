package appconfig

import (
	"os"
	"path/filepath"
	"testing"

	"eino-cli/deepagent/config"
	"eino-cli/deepagent/core/modelhub"
)

func TestLoadExpandsEnvironment(t *testing.T) {
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
	cfg, err := Load(path)
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

func TestDockerWorkspaceRequiresImage(t *testing.T) {
	base := Config{Manager: ManagerConfig{MySQLDSN: "dsn", RedisAddr: "redis"}, Models: []modelhub.Config{{Name: "primary"}}, DefaultModel: "primary", FilesystemKind: "docker"}
	err := base.Validate()
	if err == nil {
		t.Fatal("Docker workspace without image accepted")
	}
	base.Docker = config.SandboxConfig{Image: "aio-image"}
	err = base.Validate()
	if err != nil {
		t.Fatal(err)
	}
}

func TestLoadManagerOnlyAndPreservesEnvironmentValues(t *testing.T) {
	password := "secret: # value\nnext-line"
	t.Setenv("TEST_REDIS_PASSWORD", password)
	path := filepath.Join(t.TempDir(), "web.yaml")
	err := os.WriteFile(path, []byte(`manager:
  mysql_dsn: test-dsn
  redis_addr: localhost:6379
  redis_password: "${TEST_REDIS_PASSWORD}"
`), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Manager.RedisPassword != password {
		t.Fatal("environment value changed during YAML decoding")
	}
	err = cfg.Validate()
	if err == nil {
		t.Fatal("worker accepted configuration without a model")
	}
}

func TestLoadRejectsMissingManagerConnections(t *testing.T) {
	for _, data := range []string{"{}", "manager:\n  mysql_dsn: test-dsn", "manager:\n  redis_addr: localhost:6379"} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		err := os.WriteFile(path, []byte(data), 0o600)
		if err != nil {
			t.Fatal(err)
		}
		_, err = Load(path)
		if err == nil {
			t.Fatal("accepted missing manager connection")
		}
	}
}
