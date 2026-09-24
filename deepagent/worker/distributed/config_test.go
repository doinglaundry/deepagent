package distributed

import (
	"eino-cli/deepagent/config"
	"eino-cli/deepagent/core/modelhub"
	"eino-cli/deepagent/manager/compat"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadWorkerConfigExpandsEnvironmentAndPreservesModel(t *testing.T) {
	t.Setenv("TEST_WORKER_DSN", "user:pass@tcp(localhost:3306)/db")
	path := filepath.Join(t.TempDir(), "config.yaml")
	err := os.WriteFile(path, []byte("manager:\n  namespace: example\n  mysql_dsn: ${TEST_WORKER_DSN}\n  redis_addr: localhost:6379\ndefault_model: test\nmodels:\n  - name: test\n    provider: openai\n    model: test-model\nworker:\n  concurrency: 2\n  permit_ttl: 30s\n"), 0600)
	if err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Manager.MySQLDSN != os.Getenv("TEST_WORKER_DSN") || c.Worker.Concurrency != 2 || len(c.Models) != 1 || c.Models[0].Model != "test-model" {
		t.Fatalf("bad config %+v", c)
	}
}
func TestConfigRequiresExplicitConfiguredModel(t *testing.T) {
	if err := (Config{}).Validate(); err == nil {
		t.Fatal("empty configuration accepted")
	}
}

func TestEnvironmentExpansionCannotChangeYAMLStructure(t *testing.T) {
	t.Setenv("WORKER_TEST_KEY", "secret: #value\nsecond line")
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := "manager:\n  namespace: test\n  mysql_dsn: dummy\n  redis_addr: localhost:6379\ndefault_model: local\nmodels:\n  - name: local\n    model: local\n    provider: openai\n    api_key: ${WORKER_TEST_KEY}\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Models[0].APIKey != os.Getenv("WORKER_TEST_KEY") {
		t.Fatal("API key was changed by YAML parsing")
	}
}

func TestDockerWorkspaceRequiresImage(t *testing.T) {
	base := Config{Manager: manager.Config{Namespace: "default", MySQLDSN: "dsn", RedisAddr: "redis"}, Models: []modelhub.Config{{Name: "primary"}}, DefaultModel: "primary", WorkspaceKind: "docker"}
	if err := base.Validate(); err == nil {
		t.Fatal("Docker workspace without image accepted")
	}
	base.Docker = config.SandboxConfig{Image: "aio-image"}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
}
