package memory

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound  = errors.New("memory artifact not found")
	ErrLeaseLost = errors.New("memory lease lost")
	ErrConflict  = errors.New("memory lease already held")
)

type Lease struct {
	Key       string
	Token     string
	ExpiresAt time.Time
}

type Artifact struct {
	Version string
	Data    []byte
}

type Store interface {
	ClaimMemory(context.Context, string, string, time.Duration) (Lease, error)
	RenewMemory(context.Context, Lease, time.Duration) (Lease, error)
	CompleteMemory(context.Context, Lease, string, []byte) error
	ReleaseMemory(context.Context, Lease) error
	GetMemory(context.Context, string) (Artifact, error)
	ListMemory(context.Context, string, int, int) (map[string]Artifact, error)
}
