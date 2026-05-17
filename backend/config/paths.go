// Package config: paths.go derives every per-user / per-thread filesystem
// layout from cfg.RootDir. Top-level functions only — Paths used to be a
// struct that just held base_dir; that was a pure-forwarding indirection and
// violated AGENTS.md "Behavior lives in plain top-level functions".
package config

import (
	"os"
	"path/filepath"
)

// VirtualPathPrefix is what the LLM sees inside the sandbox. Host paths get
// reverse-mapped to this prefix when masking tool output.
const VirtualPathPrefix = "/mnt/user-data"

// baseDir is the on-disk root for all multi-tenant state. Lives under
// cfg.RootDir/.eino-cli to keep one repo checkout self-contained.
func baseDir(cfg *Config) string {
	return filepath.Join(cfg.RootDir, ".eino-cli")
}

func UserDir(cfg *Config, uid string) string {
	return filepath.Join(baseDir(cfg), "users", uid)
}

func ThreadDir(cfg *Config, tid, uid string) string {
	return filepath.Join(UserDir(cfg, uid), "threads", tid)
}

// SandboxUserDataDir is the host side of /mnt/user-data — the LLM-visible
// root that workspace / uploads / outputs all hang off of.
func SandboxUserDataDir(cfg *Config, tid, uid string) string {
	return filepath.Join(ThreadDir(cfg, tid, uid), "user-data")
}

func SandboxWorkDir(cfg *Config, tid, uid string) string {
	return filepath.Join(SandboxUserDataDir(cfg, tid, uid), "workspace")
}

func SandboxUploadsDir(cfg *Config, tid, uid string) string {
	return filepath.Join(SandboxUserDataDir(cfg, tid, uid), "uploads")
}

func SandboxOutputsDir(cfg *Config, tid, uid string) string {
	return filepath.Join(SandboxUserDataDir(cfg, tid, uid), "outputs")
}

// EnsureThreadDirs creates every per-thread directory that tools may write
// into. Idempotent — first call creates, subsequent calls no-op via MkdirAll.
func EnsureThreadDirs(cfg *Config, tid, uid string) error {
	for _, dir := range []string{
		SandboxWorkDir(cfg, tid, uid),
		SandboxUploadsDir(cfg, tid, uid),
		SandboxOutputsDir(cfg, tid, uid),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}
