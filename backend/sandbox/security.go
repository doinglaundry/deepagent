package sandbox

import (
	"eino-cli/backend/config"
	"eino-cli/backend/consts"
)

// HostBashDisabledMessage is the canned refusal returned when execute/shell
// run against a local manager without allow_host_bash.
const HostBashDisabledMessage = consts.HostBashDisabledMessage

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
