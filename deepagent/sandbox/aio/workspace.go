package aio

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"eino-cli/deepagent/config"
	"eino-cli/deepagent/sandbox"
)

// AcquireDockerWorkspace starts one container for a thread, mounting its root
// at the same absolute path inside the container. The caller owns release.
func AcquireDockerWorkspace(ctx context.Context, source config.SandboxConfig, sessionID, workDir string) (sandbox.Sandbox, func(), error) {
	if source.Image == "" {
		return nil, nil, fmt.Errorf("docker workspace image is required")
	}
	root, err := filepath.Abs(workDir)
	if err != nil {
		return nil, nil, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, nil, err
	}
	if !info.IsDir() {
		return nil, nil, fmt.Errorf("Docker workspace root is not a directory")
	}
	if source.ContainerPrefix == "" {
		source.ContainerPrefix = defaultContainerPrefix
	}
	source.Use = "docker"
	source.Mounts = append(append([]config.VolumeMount(nil), source.Mounts...), config.VolumeMount{HostPath: root, ContainerPath: root})
	m, err := New(&config.Config{Sandbox: source}, sessionID)
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() {
		if shutdown, ok := m.(sandbox.Shutdowner); ok {
			shutdown.Shutdown()
		}
	}
	id, err := m.GetSandboxIdBySessionId(ctx, sessionID)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	provider, err := m.Get(ctx, id)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return provider, cleanup, nil
}
