package autodream

import "time"

const (
	DefaultMinSessions  = 5
	SessionScanInterval = 10 * time.Minute
)

type ForkedAgentResult struct {
	FilesTouched []string
	Output       string
}

type SessionCandidate struct {
	SessionID string
	Mtime     time.Time
}
