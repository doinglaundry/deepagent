package agent

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"eino-cli/backend/config"
)

// LoadAgentConfigFromDir reads "<baseDir>/<name>/config.yaml" and
// returns it as a *config.AgentConfig. It mirrors the deerflow
// load_agent_config(name) "FileNotFoundError" semantics:
//
//   - name == ""              → returns nil, nil  (default agent path)
//   - directory missing       → returns nil, error (Python parity)
//   - config.yaml missing     → returns nil, error
//   - parse error             → returns nil, error
//   - success                 → returns *config.AgentConfig, nil
//
// The caller is expected to have already run ValidateAgentName on the
// input.
func LoadAgentConfigFromDir(baseDir, name string) (*config.AgentConfig, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	if strings.TrimSpace(baseDir) == "" {
		return nil, fmt.Errorf("agent dir is empty (cfg.AgentsDir not set)")
	}

	agentDir := filepath.Join(baseDir, strings.ToLower(name))
	info, err := os.Stat(agentDir)
	switch {
	case err != nil && errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("agent directory not found: %s", agentDir)
	case err != nil:
		return nil, fmt.Errorf("stat agent directory %s: %w", agentDir, err)
	case !info.IsDir():
		return nil, fmt.Errorf("agent path is not a directory: %s", agentDir)
	}

	configFile := filepath.Join(agentDir, "config.yaml")
	data, err := os.ReadFile(configFile)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("agent config not found: %s", configFile)
		}
		return nil, fmt.Errorf("read agent config %s: %w", configFile, err)
	}

	var ac config.AgentConfig
	if err = yaml.Unmarshal(data, &ac); err != nil {
		return nil, fmt.Errorf("parse agent config %s: %w", configFile, err)
	}

	// YAML may omit the name (file path is the de-facto identifier);
	// fall back to the lookup key. Trim Model so accidental
	// trailing whitespace from the YAML literal doesn't shadow a
	// real model name in the resolver.
	ac.Name = nameOrFallback(ac.Name, name)
	ac.Model = strings.TrimSpace(ac.Model)
	return &ac, nil
}

// LoadAgentConfigFromConfig looks up a custom agent inside an already
// loaded config.Config (i.e. the inline "agents:" YAML block). Returns
// nil + nil for "no such inline entry" so callers can fall back to
// LoadAgentConfigFromDir.
func LoadAgentConfigFromConfig(cfg config.Config, name string) (*config.AgentConfig, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	ac, ok := cfg.Agents[name]
	if !ok {
		return nil, nil
	}
	ac.Name = nameOrFallback(ac.Name, name)
	ac.Model = strings.TrimSpace(ac.Model)
	return &ac, nil
}

// nameOrFallback trims the candidate and returns it, falling back to
// the supplied default when the trimmed result is empty. Pulled out
// because both loaders need the exact same "use the YAML name if it
// has one, otherwise use the lookup key" rule and writing the rule
// twice was load-bearing for two readability bugs (a redundant second
// TrimSpace call and a two-branch assignment that re-wrote ac.Name in
// both arms).
func nameOrFallback(candidate, fallback string) string {
	trimmed := strings.TrimSpace(candidate)
	if trimmed == "" {
		return fallback
	}
	return trimmed
}

// LoadAgentProfile is the high-level resolver used by MakeLeadAgent.
// It mirrors deerflow's load_agent_config behaviour with one extension:
// inline cfg.Agents entries take precedence over per-directory YAML so
// users can fully describe simple agents in the main config.yaml.
//
// Resolution order:
//  1. cfg.Agents[name] (inline)
//  2. cfg.AgentsDir/<name>/config.yaml (directory)
//  3. nil + nil  (no custom profile — fall back to defaults)
func LoadAgentProfile(cfg config.Config, name string) (*config.AgentConfig, error) {
	// ValidateAgentName trims, accepts empty as the "use defaults"
	// sentinel ("", nil), and rejects bad characters with an error.
	// LoadAgentConfigFrom{Config,Dir} both early-return nil for empty
	// names too, so we don't need our own short-circuit.
	name, err := ValidateAgentName(name)
	if err != nil {
		return nil, err
	}
	if profile, err := LoadAgentConfigFromConfig(cfg, name); err != nil {
		return nil, err
	} else if profile != nil {
		return profile, nil
	}
	if strings.TrimSpace(cfg.AgentsDir) == "" {
		return nil, nil
	}
	// Allow the directory loader to fail soft when the agent dir is
	// missing — mirrors Python's "no custom agent configured" branch.
	profile, err := LoadAgentConfigFromDir(cfg.AgentsDir, name)
	if err != nil {
		// Distinguish "missing" (soft) from "malformed" (hard).
		var pathErr *os.PathError
		if errors.As(err, &pathErr) && errors.Is(pathErr.Err, fs.ErrNotExist) {
			return nil, nil
		}
		// Our LoadAgentConfigFromDir wraps not-found as a plain error
		// without preserving fs.ErrNotExist; sniff the message instead.
		if strings.Contains(err.Error(), "not found") {
			return nil, nil
		}
		return nil, err
	}
	return profile, nil
}
