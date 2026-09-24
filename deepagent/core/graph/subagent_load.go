package graph

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"eino-cli/deepagent/core/tools"
	"github.com/cloudwego/eino/schema"
	"gopkg.in/yaml.v3"
)

// loadSubAgentsFromDir reads dir/name/SUBAGENT.yaml in deterministic name order.
func loadSubAgentsFromDir(ctx context.Context, dir string) ([]*SubAgent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, err
	}
	var result []*SubAgent
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(entry.Name(), "SUBAGENT.yaml")
		file, err := root.Open(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("subagent %s: %w", path, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(file, 64*1024+1))
		closeErr := file.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if len(data) > 64*1024 {
			return nil, fmt.Errorf("subagent %s exceeds 64 KiB", path)
		}
		var spec struct {
			Name             string   `yaml:"name"`
			SystemPrompt     string   `yaml:"system_prompt"`
			MaxSteps         int      `yaml:"max_steps"`
			EnableFilesystem bool     `yaml:"enable_filesystem"`
			EnableWeb        bool     `yaml:"enable_web"`
			ReadOnly         bool     `yaml:"read_only"`
			Tools            []string `yaml:"tools"`
		}
		decoder := yaml.NewDecoder(strings.NewReader(string(data)))
		decoder.KnownFields(true)
		if err := decoder.Decode(&spec); err != nil {
			return nil, fmt.Errorf("subagent %s: %w", path, err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return nil, fmt.Errorf("subagent %s must contain one YAML document", path)
		}
		if spec.Name == "" {
			spec.Name = entry.Name()
		}
		if strings.TrimSpace(spec.Name) == "" || strings.TrimSpace(spec.SystemPrompt) == "" || spec.MaxSteps < 0 {
			return nil, fmt.Errorf("subagent %s requires a name, system_prompt and non-negative max_steps", path)
		}
		var mask tools.Mask
		if spec.Tools != nil {
			names := make(map[string]bool, len(spec.Tools))
			for _, name := range spec.Tools {
				if strings.TrimSpace(name) == "" || names[name] {
					return nil, fmt.Errorf("subagent %s has empty or duplicate tool name", path)
				}
				names[name] = true
			}
			mask = func(_ context.Context, info *schema.ToolInfo) bool { return names[info.Name] }
		}
		result = append(result, &SubAgent{Name: spec.Name, SystemPrompt: spec.SystemPrompt, MaxSteps: spec.MaxSteps, EnableFilesystem: spec.EnableFilesystem, EnableWeb: spec.EnableWeb, ReadOnly: spec.ReadOnly, ToolMask: mask})
	}
	return result, nil
}
