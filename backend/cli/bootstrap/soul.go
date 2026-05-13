package bootstrap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"eino-cli/backend/agent"
	"eino-cli/backend/config"
	"eino-cli/backend/soulbootstrap"
)

var buildSoulBootstrapReply = agent.BuildSoulBootstrapReply

type Session struct {
	path  string
	state soulbootstrap.State
}

func NewSession(cfg *config.Config) (*Session, error) {
	path := getSoulPath(cfg)
	existing, err := loadExistingSoul(path)
	if err != nil {
		return nil, err
	}
	return &Session{
		path: path,
		state: soulbootstrap.State{
			Phase:        soulbootstrap.PhaseHello,
			ExistingSoul: existing,
		},
	}, nil
}

func (s *Session) Next(ctx context.Context, cfg *config.Config, userInput string) (soulbootstrap.Reply, error) {
	if strings.TrimSpace(userInput) != "" {
		s.state.Conversation = append(s.state.Conversation, soulbootstrap.Turn{
			Role:    "user",
			Content: strings.TrimSpace(userInput),
		})
	}
	reply, err := buildSoulBootstrapReply(ctx, cfg, s.state)
	if err != nil {
		return soulbootstrap.Reply{}, err
	}
	s.state.Fields = reply.Fields
	s.state.Phase = reply.NextPhase
	s.state.Round++
	if strings.TrimSpace(reply.Message) != "" {
		s.state.Conversation = append(s.state.Conversation, soulbootstrap.Turn{
			Role:    "assistant",
			Content: strings.TrimSpace(reply.Message),
		})
	}
	if strings.TrimSpace(reply.Draft) != "" {
		s.state.Draft = strings.TrimSpace(reply.Draft)
	}
	return reply, nil
}

func (s *Session) Save() error {
	return writeSoulFile(s.path, s.state.Draft)
}

func (s *Session) HasDraft() bool {
	return strings.TrimSpace(s.state.Draft) != ""
}

func (s *Session) Draft() string {
	return s.state.Draft
}

func getSoulPath(cfg *config.Config) string {
	return filepath.Join(cfg.RootDir, "yaml", "soul.md")
}

func loadExistingSoul(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return strings.TrimSpace(string(data)), nil
	}
	if os.IsNotExist(err) {
		return "", nil
	}
	return "", fmt.Errorf("read soul.md: %w", err)
}

func writeSoulFile(path string, body string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir yaml: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.TrimSpace(body)+"\n"), 0o600); err != nil {
		return fmt.Errorf("write temp soul.md: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename soul.md: %w", err)
	}
	return nil
}
