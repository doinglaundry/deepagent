package tools

import (
	"context"
	"fmt"
	"strings"

	"eino-cli/backend/consts"
	runtimecontext "eino-cli/backend/runtime/context"
	"eino-cli/backend/sandbox"
	"eino-cli/backend/sandboxpaths"
)

type hostSearchRoot struct {
	HostPath string
	Mappings []sandboxpaths.MountMapping
}

func buildAbsoluteVirtualPath(toolPath string) (string, error) {
	if strings.HasPrefix(toolPath, "/") {
		return toolPath, nil
	}
	return sandboxpaths.VirtualPathPrefixRepo + "/" + strings.TrimPrefix(toolPath, "/"), nil
}

func validateVirtualPath(virtualPath string, readOnly bool) error {
	if strings.Contains(virtualPath, "..") {
		return fmt.Errorf("path traversal: %s", virtualPath)
	}
	if strings.HasPrefix(virtualPath, sandboxpaths.VirtualPathPrefixSkills) {
		if readOnly {
			return nil
		}
		return fmt.Errorf("write not allowed: %s", virtualPath)
	}
	for _, prefix := range []string{
		sandboxpaths.VirtualPathPrefixRepo,
		sandboxpaths.VirtualPathPrefixWorkspace,
		sandboxpaths.VirtualPathPrefixUploads,
		sandboxpaths.VirtualPathPrefixOutputs,
	} {
		if virtualPath == prefix || strings.HasPrefix(virtualPath, prefix+"/") {
			return nil
		}
	}
	return fmt.Errorf("path not under allowed /mnt/*: %s", virtualPath)
}

func resolveToolSearchPath(toolPath string, readOnly bool) (string, error) {
	if strings.TrimSpace(toolPath) == "" {
		return sandboxpaths.VirtualPathPrefixRepo, nil
	}
	return resolveToolPath(toolPath, readOnly)
}

func resolveToolPath(toolPath string, readOnly bool) (virtualPath string, err error) {
	toolPath = strings.TrimSpace(toolPath)
	if toolPath == "" {
		return "", fmt.Errorf("path must not be empty")
	}
	virtualPath, err = buildAbsoluteVirtualPath(toolPath)
	if err != nil {
		return "", err
	}
	if err := validateVirtualPath(virtualPath, readOnly); err != nil {
		return "", err
	}
	return virtualPath, nil
}

func buildSandboxCommand(command, workingDir string, mappings []sandboxpaths.MountMapping) (string, error) {
	virtualWorkingDir, err := resolveToolSearchPath(workingDir, true)
	if err != nil {
		return "", err
	}
	virtualCommand := sandbox.MaskHostPathsInOutput(mappings, command)
	return "cd " + shellQuote(virtualWorkingDir) + " && " + virtualCommand, nil
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func resolveHostSearchRoot(ctx context.Context, manager sandbox.SandboxManager, toolPath string, readOnly bool) (string, error) {
	hostRoot, err := getHostSearchRoot(ctx, manager, toolPath, readOnly)
	if err != nil {
		return "", err
	}
	return hostRoot.HostPath, nil
}

func getHostSearchRoot(ctx context.Context, manager sandbox.SandboxManager, toolPath string, readOnly bool) (hostSearchRoot, error) {
	if !hasSandboxManager(manager) {
		if strings.TrimSpace(toolPath) == "" {
			return hostSearchRoot{HostPath: resolveRoot()}, nil
		}
		hostPath, err := getResolvedPath(toolPath)
		return hostSearchRoot{HostPath: hostPath}, err
	}
	virtualPath, err := resolveToolSearchPath(toolPath, readOnly)
	if err != nil {
		return hostSearchRoot{}, err
	}
	sessionID := runtimecontext.GetSessionID(ctx)
	if sessionID == "" {
		sessionID = consts.DefaultSessionID
	}
	mappings, err := sandboxpaths.BuildMountMappings(sessionID)
	if err != nil {
		return hostSearchRoot{}, err
	}
	hostPath, err := sandboxpaths.GetHostPath(mappings, virtualPath)
	if err != nil {
		return hostSearchRoot{}, err
	}
	return hostSearchRoot{HostPath: hostPath, Mappings: mappings}, nil
}
