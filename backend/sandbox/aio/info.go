// Package aio implements Sandbox over the agent-sandbox container's HTTP API.
package aio

import "time"

// SandboxInfo is the in-memory record kept per live/warm container.
type SandboxInfo struct {
	SandboxID     string
	SandboxURL    string
	ContainerName string
	ContainerID   string
	CreatedAt     time.Time
}

// warmEntry is a released sandbox whose container is still up for re-acquire.
type warmEntry struct {
	info       SandboxInfo
	releasedAt time.Time
}

const (
	defaultContainerPrefix = "eino-sandbox"
	idleCheckInterval      = 60 * time.Second
)
