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

// agentYAMLFile mirrors the on-disk layout of
// "<agents_dir>/<name>/config.yaml". It mirrors the deerflow Python
// AgentConfig pydantic model so existing deer-flow agent directories can
// be reused as-is.
type agentYAMLFile struct {
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description"`
	Instruction  string   `yaml:"instruction"`
	MaxIteration int      `yaml:"max_iteration"`
	Model        string   `yaml:"model"`
	ToolGroups   []string `yaml:"tool_groups"`
	Skills       []string `yaml:"skills"`
}

// LoadAgentConfigFromDir reads "<baseDir>/<name>/config.yaml" and
// projects it onto an AgentProfile. It mirrors the
// deerflow load_agent_config(name) "FileNotFoundError" semantics:
//
//   - name == ""              → returns nil, nil  (default agent path)
//   - directory missing       → returns nil, error (Python parity)
//   - config.yaml missing     → returns nil, error
//   - parse error             → returns nil, error
//   - success                 → returns *AgentProfile, nil
//
// The caller is expected to have already run ValidateAgentName on the
// input.
func LoadAgentConfigFromDir(baseDir, name string) (*AgentProfile, error) {
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

	var f agentYAMLFile
	if err = yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse agent config %s: %w", configFile, err)
	}

	resolvedName := strings.TrimSpace(f.Name)
	if resolvedName == "" {
		resolvedName = name
	}
	return &AgentProfile{
		Name:        resolvedName,
		Model:       strings.TrimSpace(f.Model),
		ToolGroups:  cloneStringSlicePreservingNil(f.ToolGroups),
		Skills:      cloneStringSlicePreservingNil(f.Skills),
		Instruction: f.Instruction,
	}, nil
}

// LoadAgentConfigFromConfig looks up a custom agent inside an already
// loaded config.Config (i.e. the inline "agents:" YAML block). Returns
// nil + nil for "no such inline entry" so callers can fall back to
// LoadAgentConfigFromDir.
func LoadAgentConfigFromConfig(cfg config.Config, name string) (*AgentProfile, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	ac, ok := cfg.Agents[name]
	if !ok {
		// Try the agent's own Name field as a secondary key — useful when
		// the inline YAML uses arbitrary map keys.
		for _, candidate := range cfg.Agents {
			if strings.EqualFold(strings.TrimSpace(candidate.Name), name) {
				ac = candidate
				ok = true
				break
			}
		}
	}
	if !ok {
		return nil, nil
	}
	return &AgentProfile{
		Name:        firstNonEmpty(ac.Name, name),
		Model:       strings.TrimSpace(ac.Model),
		ToolGroups:  cloneStringSlicePreservingNil(ac.ToolGroups),
		Skills:      cloneStringSlicePreservingNil(ac.Skills),
		Instruction: ac.Instruction,
	}, nil
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
func LoadAgentProfile(cfg config.Config, name string) (*AgentProfile, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	if _, err := ValidateAgentName(name); err != nil {
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

func cloneStringSlicePreservingNil(src []string) []string {
	if src == nil {
		return nil
	}
	out := make([]string, len(src))
	copy(out, src)
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}
