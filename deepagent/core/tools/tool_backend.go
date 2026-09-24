package tools

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"eino-cli/deepagent/core/backend"
	"github.com/cloudwego/eino/schema"
)

func requireBackend(b backend.Backend) error {
	if b == nil {
		return fmt.Errorf("backend is required")
	}
	return nil
}

func decodeToolArgs(args string, target any) error {
	var values map[string]json.RawMessage
	if err := json.Unmarshal([]byte(args), &values); err != nil {
		return err
	}
	for canonical, alias := range map[string]string{"path": "file_path", "old": "old_string", "new": "new_string"} {
		if _, exists := values[canonical]; !exists {
			if value, ok := values[alias]; ok {
				values[canonical] = value
			}
		}
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return err
	}
	return nil
}

func relativePath(path string) error {
	if filepath.IsAbs(path) {
		return fmt.Errorf("path must be relative to working directory")
	}
	return nil
}

func toolInfo(name, desc string, params map[string]*schema.ParameterInfo) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: name, Desc: desc, ParamsOneOf: schema.NewParamsOneOfByParams(params)}, nil
}
