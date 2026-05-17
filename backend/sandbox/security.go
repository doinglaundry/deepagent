package sandbox

import "eino-cli/backend/config"

// HostBashDisabledMessage is shown when a tool tries to spawn a host shell
// while the manager is the local (un-sandboxed) one. Copy lives here, not
// in tools, so the wording stays consistent across execute/shell/subagent.
const HostBashDisabledMessage = "Host bash execution is disabled for LocalSandboxManager because it is not a secure sandbox boundary. Switch to AioSandboxManager (sandbox.use=aio) for isolated bash access, or set sandbox.allow_host_bash: true only in a fully trusted local environment."

// UsesLocalSandboxManager: the only "this is the host fs" predicate the
// tool / middleware layer needs. cfg.Sandbox.Use == "" defaults to local
// (factory.go), so empty string counts as local too.
func UsesLocalSandboxManager(cfg *config.Config) bool {
	if cfg == nil {
		return true
	}
	use := cfg.Sandbox.Use
	return use == "" || use == "local"
}

// IsHostBashAllowed gates execute/shell tools. Returns true when:
//   - the manager isn't the local one (any real sandbox is fine), OR
//   - the user explicitly opted in via sandbox.allow_host_bash.
//
// Nil cfg is conservative — bash stays disabled.
func IsHostBashAllowed(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	if !UsesLocalSandboxManager(cfg) {
		return true
	}
	return cfg.Sandbox.AllowHostBash
}
