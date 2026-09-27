package tools

import (
	"fmt"
	"path/filepath"

	"eino-cli/deepagent/core/backend"
	"github.com/cloudwego/eino/schema"
)

func requireBackend(b backend.Filesystem) error {
	if b == nil {
		return fmt.Errorf("backend is required")
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
