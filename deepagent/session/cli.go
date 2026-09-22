package session

import (
	"context"
	"fmt"

	"eino-cli/deepagent/config"
	"eino-cli/deepagent/constant"
)

// StartSession ensures on-disk session dirs exist and returns the session id.
func StartSession(ctx context.Context) (string, error) {
	sessionID := consts.DefaultSessionID
	if err := config.EnsureSessionDirs(sessionID); err != nil {
		return "", fmt.Errorf("session: ensure dirs: %w", err)
	}
	return sessionID, nil
}
