package sandbox

import "eino-cli/backend/config"

// HostBashDisabledMessage is the canned refusal returned when execute/shell
// run against a local manager without allow_host_bash.
const HostBashDisabledMessage = "Host bash execution is disabled for LocalSandboxManager because it is not a secure sandbox boundary. Switch to AioSandboxManager (sandbox.use=aio) for isolated bash access, or set sandbox.allow_host_bash: true only in a fully trusted local environment."

// UsesLocalSandboxManager reports whether cfg selects the host-fs provider.
func UsesLocalSandboxManager(cfg *config.Config) bool {
	if cfg == nil {
		return true
	}
	use := cfg.Sandbox.Use
	return use == "" || use == "local"
}

// IsHostBashAllowed reports whether execute/shell may spawn the host shell.
func IsHostBashAllowed(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	if !UsesLocalSandboxManager(cfg) {
		return true
	}
	return cfg.Sandbox.AllowHostBash
}
