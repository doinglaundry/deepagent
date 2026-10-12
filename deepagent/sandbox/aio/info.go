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

// managedContainer 是本会话唯一的容器；sandbox 为 nil 时已释放，但容器仍可复用。
type managedContainer struct {
	sandbox   *Sandbox
	info      SandboxInfo
	idleSince time.Time // 访问或释放时重新计时；释放后保留一个完整的空闲窗口。
}

const (
	defaultContainerPrefix = "eino-sandbox"
	idleCheckInterval      = 60 * time.Second
)
