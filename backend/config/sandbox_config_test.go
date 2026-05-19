package config

import (
	"testing"
	"time"

	"eino-cli/backend/consts"
)

// TestNormalizeSandbox_DefaultsAndOverrides locks the contract Provider-side
// code relies on: zero-value fields get filled, user-set fields are kept.
// If this drifts, the aio manager's "read cfg.Image / cfg.IdleTimeout
// directly" assumption breaks and a fresh install boots with an empty
// container image.
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
