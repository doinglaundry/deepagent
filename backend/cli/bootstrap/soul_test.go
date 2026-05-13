package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eino-cli/backend/config"
	"eino-cli/backend/soulbootstrap"
)

func TestNewSessionLoadsExistingSoul(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "yaml", "soul.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("existing soul\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	session, err := NewSession(&config.Config{RootDir: root})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if session.state.ExistingSoul != "existing soul" {
		t.Fatalf("ExistingSoul = %q", session.state.ExistingSoul)
	}
}

func TestSessionNextUpdatesStateAndSavesDraft(t *testing.T) {
	old := buildSoulBootstrapReply
	buildSoulBootstrapReply = func(_ context.Context, _ *config.Config, state soulbootstrap.State) (soulbootstrap.Reply, error) {
		if len(state.Conversation) != 1 || state.Conversation[0].Content != "中文" {
			t.Fatalf("conversation not updated: %+v", state.Conversation)
		}
		return soulbootstrap.Reply{
			Message:   "draft ready",
			NextPhase: soulbootstrap.PhaseDraft,
			Fields:    soulbootstrap.Fields{PreferredLanguage: "中文"},
			Draft:     "**Identity**\nAlice",
			Ready:     true,
		}, nil
	}
	t.Cleanup(func() { buildSoulBootstrapReply = old })

	cfg := &config.Config{RootDir: t.TempDir()}
	session, err := NewSession(cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	reply, err := session.Next(context.Background(), cfg, "中文")
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if !reply.Ready || !session.HasDraft() {
		t.Fatalf("expected ready draft, reply=%+v draft=%q", reply, session.Draft())
	}
	if err := session.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(cfg.RootDir, "yaml", "soul.md"))
	if err != nil {
		t.Fatalf("read soul.md: %v", err)
	}
	if !strings.Contains(string(data), "**Identity**\nAlice") {
		t.Fatalf("soul.md = %q", string(data))
	}
}
