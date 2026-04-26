package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGatewayDisabledNoop(t *testing.T) {
	g := New("", 0)
	items, err := g.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected empty tools, got %d", len(items))
	}
}

func TestGatewayListToolsAndInvoke(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/tools":
			_, _ = w.Write([]byte(`{"tools":[{"name":"remote_read","description":"remote","risk_level":"low"}]}`))
		case "/tools/remote_read/invoke":
			_, _ = w.Write([]byte(`{"output":"plugin-ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	g := New(server.URL, 3)
	if err := g.Check(context.Background()); err != nil {
		t.Fatalf("Check() error = %v", err)
	}

	items, err := g.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(items))
	}
	if items[0].Name != "remote_read" {
		t.Fatalf("unexpected tool name: %q", items[0].Name)
	}

	result, err := g.InvokeTool(context.Background(), "remote_read", []string{"a"})
	if err != nil {
		t.Fatalf("InvokeTool() error = %v", err)
	}
	if result.Output != "plugin-ok" {
		t.Fatalf("unexpected invoke output: %q", result.Output)
	}
}
