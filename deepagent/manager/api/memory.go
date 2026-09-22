package api

import (
	"context"
	memorypkg "eino-cli/deepagent/protocol/memory"
	"time"
)

// MemoryStore is optional independent coordination for extraction and consolidation.
// Keys should group sources under a stable user prefix; the Manager adds Namespace.
type MemoryStore = memorypkg.Store
type MemoryLease = memorypkg.Lease
type MemoryArtifact = memorypkg.Artifact

// MemorySourceStore discovers persisted histories for periodic extraction.
// Only inactive threads with nonempty histories are returned.
type MemorySourceStore interface {
	ListMemorySources(context.Context, int, int) ([]MemorySource, error)
}
type MemorySource struct {
	ThreadID, SessionID string
	History             History
	UpdatedAt           time.Time
}
