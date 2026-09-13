package api

import (
	"context"
	"time"
)

// MemoryStore is optional independent coordination for extraction and consolidation.
// Keys should group sources under a stable user prefix; the Manager adds Namespace.
type MemoryStore interface {
	ClaimMemory(context.Context, string, string, time.Duration) (MemoryLease, error)
	RenewMemory(context.Context, MemoryLease, time.Duration) (MemoryLease, error)
	CompleteMemory(context.Context, MemoryLease, string, []byte) error
	ReleaseMemory(context.Context, MemoryLease) error
	GetMemory(context.Context, string) (MemoryArtifact, error)
	ListMemory(context.Context, string, int, int) (map[string]MemoryArtifact, error)
}
type MemoryLease struct {
	Key, Token string
	ExpiresAt  time.Time
}
type MemoryArtifact struct {
	Version string
	Data    []byte
}

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
