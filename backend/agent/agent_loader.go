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

	ac.Name = nameOrFallback(ac.Name, name)
	ac.Model = strings.TrimSpace(ac.Model)
	return &ac, nil
}

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

func nameOrFallback(candidate, fallback string) string {
	trimmed := strings.TrimSpace(candidate)
	if trimmed == "" {
		return fallback
	}
	return trimmed
}

func GetAgentConfig(cfg config.Config, name string) (*config.AgentConfig, error) {

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

	profile, err := LoadAgentConfigFromDir(cfg.AgentsDir, name)
	if err != nil {
		return nil, err
	}
	return profile, nil
}
