// Package aio implements Sandbox / SandboxManager on top of the
// "agent-sandbox" Docker / Apple Container image deer-flow runs as its
// default isolation boundary. Containers are HTTP-addressable; the
// manager spawns / discovers them and the sandbox struct in sandbox.go
// translates the public Sandbox interface to that HTTP API.
package aio

import "time"

// SandboxInfo is the steady-state record we keep about a live (or warm-
// pooled) container. Persisted only in-memory for now; multi-process
// discovery uses `docker ps --filter name=` instead of a shared store.
type SandboxInfo struct {
	SandboxID     string
	SandboxURL    string // e.g. http://localhost:8081
	ContainerName string
	ContainerID   string
	CreatedAt     time.Time
}

// warmEntry: a sandbox that was released by the agent but whose container
// is still running — Acquire-then-release-then-reacquire short-circuits
// the cold-start cost.
type warmEntry struct {
	info       SandboxInfo
	releasedAt time.Time
}

// Containers we manage are tagged with a name prefix (default
// "eino-sandbox") and a short hash of the thread_id. Cross-process
// discovery uses the prefix + sha hash to find sibling containers
// without a shared registry.
const (
	defaultContainerPrefix = "eino-sandbox"
	idleCheckInterval      = 60 * time.Second
)
