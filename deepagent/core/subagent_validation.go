package deepagents

import "fmt"

// validateSubAgentNames 校验 SubAgent 名称不能重复。
// 该校验应在 SubAgentsDirs 加载完成后调用。
func validateSubAgentNames(config *Config) error {
	if len(config.SubAgents) == 0 {
		return nil
	}
	names := make(map[string]bool)
	for _, sa := range config.SubAgents {
		if sa.Name == "" {
			continue
		}
		if names[sa.Name] {
			return fmt.Errorf("duplicate sub agent name %q", sa.Name)
		}
		names[sa.Name] = true
	}
	return nil
}
