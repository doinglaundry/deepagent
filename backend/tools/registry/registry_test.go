package registry

import (
	"testing"

	"eino-cli/backend/config"
)

func TestRegistryBuiltinTools(t *testing.T) {
	r := New()

	readTool, err := r.Get("read")
	if err != nil {
		t.Fatalf("Get(read) error = %v", err)
	}
	if readTool.Source != "builtin" {
		t.Fatalf("unexpected source: %q", readTool.Source)
	}

	items, err := r.GetAvailableTools(config.Config{}, "", false)
	if err != nil {
		t.Fatalf("GetAvailableTools() error = %v", err)
	}
	if len(items) < 3 {
		t.Fatalf("expected at least 3 builtin tools, got %d", len(items))
	}
}
