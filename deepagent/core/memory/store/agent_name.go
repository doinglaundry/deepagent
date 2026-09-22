package store

import (
	"fmt"
	"regexp"
)

// agentNamePattern mirrors deer-flow AGENT_NAME_PATTERN; alnum start, then
// alnum/underscore/hyphen, max 64 chars total. Empty agentName means "global"
// and is permitted by validateAgentName via short-circuit.
var agentNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_\-]{0,63}$`)

// validateAgentName lets "" through (== global memory) and rejects anything
// that could escape the memory dir or collide with the global file.
func validateAgentName(name string) error {
	if name == "" {
		return nil
	}
	if !agentNamePattern.MatchString(name) {
		return fmt.Errorf("invalid agent name %q: must match %s", name, agentNamePattern.String())
	}
	return nil
}
