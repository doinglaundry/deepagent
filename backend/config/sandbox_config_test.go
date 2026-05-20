package config

import (
	"testing"
	"time"

	"eino-cli/backend/consts"

	"gopkg.in/yaml.v3"
)

func TestNormalizeSandbox_DefaultsAndOverrides(t *testing.T) {
	t.Run("fills zero values", func(t *testing.T) {
		s := &SandboxConfig{}
		normalizeSandbox(s)

		if s.Image != consts.DefaultSandboxImage {
			t.Errorf("Image = %q, want %q", s.Image, consts.DefaultSandboxImage)
		}
		if s.ContainerPrefix != consts.DefaultSandboxContainerPrefix {
			t.Errorf("ContainerPrefix = %q, want %q", s.ContainerPrefix, consts.DefaultSandboxContainerPrefix)
		}
		if s.IdleTimeout != consts.DefaultSandboxIdleTimeout {
			t.Errorf("IdleTimeout = %v, want %v", s.IdleTimeout, consts.DefaultSandboxIdleTimeout)
		}
		if s.Replicas != consts.DefaultSandboxReplicas {
			t.Errorf("Replicas = %d, want %d", s.Replicas, consts.DefaultSandboxReplicas)
		}
	})

	t.Run("preserves user-set values", func(t *testing.T) {
		s := &SandboxConfig{
			Image:           "custom-image:tag",
			ContainerPrefix: "my-prefix",
			IdleTimeout:     5 * time.Minute,
			Replicas:        7,
		}
		normalizeSandbox(s)

		if s.Image != "custom-image:tag" {
			t.Errorf("Image overwritten: %q", s.Image)
		}
		if s.ContainerPrefix != "my-prefix" {
			t.Errorf("ContainerPrefix overwritten: %q", s.ContainerPrefix)
		}
		if s.IdleTimeout != 5*time.Minute {
			t.Errorf("IdleTimeout overwritten: %v", s.IdleTimeout)
		}
		if s.Replicas != 7 {
			t.Errorf("Replicas overwritten: %d", s.Replicas)
		}
	})

	t.Run("does not touch unrelated fields", func(t *testing.T) {
		s := &SandboxConfig{Use: "local", AllowHostBash: true}
		normalizeSandbox(s)

		if s.Use != "local" {
			t.Errorf("Use mutated: %q", s.Use)
		}
		if !s.AllowHostBash {
			t.Error("AllowHostBash flipped to false")
		}
	})
}

func TestConfigParsesSandbox(t *testing.T) {
	var cfg Config
	err := yaml.Unmarshal([]byte(`
default_model: test
log_level: debug
models:
  - name: test
    provider: openai
    model: gpt
    base_url: https://example.com
    api_key: key
sandbox:
  use: aio
  allow_host_bash: true
  image: custom
  container_prefix: prefix
  replicas: 2
  idle_timeout: 5m
  mounts:
    - host_path: /tmp
      container_path: /mnt/tmp
      read_only: true
  environment:
    KEY: value
`), &cfg)
	if err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if cfg.LogLevel != "debug" {
		t.Fatalf("LogLevel = %q, want debug", cfg.LogLevel)
	}
	if cfg.Sandbox.Use != "aio" {
		t.Fatalf("Sandbox.Use = %q, want aio", cfg.Sandbox.Use)
	}
	if !cfg.Sandbox.AllowHostBash {
		t.Fatal("AllowHostBash should parse true")
	}
	if cfg.Sandbox.Image != "custom" || cfg.Sandbox.ContainerPrefix != "prefix" {
		t.Fatalf("sandbox image/prefix mismatch: %+v", cfg.Sandbox)
	}
	if cfg.Sandbox.IdleTimeout != 5*time.Minute || cfg.Sandbox.Replicas != 2 {
		t.Fatalf("sandbox timeout/replicas mismatch: %+v", cfg.Sandbox)
	}
	if len(cfg.Sandbox.Mounts) != 1 || cfg.Sandbox.Mounts[0].ContainerPath != "/mnt/tmp" || !cfg.Sandbox.Mounts[0].ReadOnly {
		t.Fatalf("sandbox mounts mismatch: %+v", cfg.Sandbox.Mounts)
	}
	if cfg.Sandbox.Environment["KEY"] != "value" {
		t.Fatalf("sandbox env mismatch: %+v", cfg.Sandbox.Environment)
	}
}
