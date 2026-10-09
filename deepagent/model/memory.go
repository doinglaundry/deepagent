package model

import (
	"context"
	"errors"
	"time"
)

type MemoryService interface {
	Read(ctx context.Context, scope string) (*MemorySnapshot, error)
	Observe(ctx context.Context, scope, threadID string, messages []*Message) error
	Consolidate(ctx context.Context, scope string) error
}

type MemorySnapshot struct {
	Scope     string
	Summary   string
	UpdatedAt time.Time
}

var (
	ErrMemoryNotFound  = errors.New("memory artifact not found")
	ErrMemoryLeaseLost = errors.New("memory lease lost")
	ErrMemoryConflict  = errors.New("memory lease already held")
)

type MemoryLease struct {
	Key       string
	Token     string
	ExpiresAt time.Time
}

type MemoryArtifact struct {
	Version string
	Data    []byte
}

type MemoryStore interface {
	ClaimMemory(context.Context, string, string, time.Duration) (MemoryLease, error)
	RenewMemory(context.Context, MemoryLease, time.Duration) (MemoryLease, error)
	CompleteMemory(context.Context, MemoryLease, string, []byte) error
	ReleaseMemory(context.Context, MemoryLease) error
	GetMemory(context.Context, string) (MemoryArtifact, error)
	ListMemory(context.Context, string, int, int) (map[string]MemoryArtifact, error)
}
