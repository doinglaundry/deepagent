package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"eino-cli/internal/config"
	"eino-cli/internal/plugin/gateway"
)

func TestRegistryMergesBuiltinAndPluginTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tools":
			_, _ = w.Write([]byte(`[{"name":"read","description":"plugin-read"},{"name":"remote_exec","description":"plugin-exec","risk_level":"high"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	r := New(gateway.New(config.PluginGatewayConfig{Enabled: true, Endpoint: server.URL, TimeoutSeconds: 3}))
	if err := r.RefreshPluginTools(context.Background()); err != nil {
		t.Fatalf("RefreshPluginTools() error = %v", err)
	}

	readTool, err := r.Get("read")
	if err != nil {
		t.Fatalf("Get(read) error = %v", err)
	}
	if readTool.Source != "builtin" {
		t.Fatalf("builtin tool should win dedup, got source=%q", readTool.Source)
	}

	remoteTool, err := r.Get("remote_exec")
	if err != nil {
		t.Fatalf("Get(remote_exec) error = %v", err)
	}
	if remoteTool.Source != "plugin" {
		t.Fatalf("unexpected remote tool source: %q", remoteTool.Source)
	}

	items, err := r.GetAvailableTools(config.Config{}, "", false)
	if err != nil {
		t.Fatalf("GetAvailableTools() error = %v", err)
	}
	if len(items) < 4 {
		t.Fatalf("expected builtin+plugin tools, got %d", len(items))
	}
}
