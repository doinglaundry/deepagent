package aio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// containerRuntime is "docker" or macOS 14+ "container" CLI.
type containerRuntime string

const (
	runtimeDocker    containerRuntime = "docker"
	runtimeContainer containerRuntime = "container"
)

func detectRuntime() containerRuntime {
	if _, err := exec.LookPath("docker"); err == nil {
		return runtimeDocker
	}
	if _, err := exec.LookPath("container"); err == nil {
		return runtimeContainer
	}
	return ""
}

type containerSpec struct {
	Runtime containerRuntime
	Image   string
	Name    string
	Port    int
	Mounts  []mountSpec
	Env     map[string]string
}

type mountSpec struct {
	Host      string
	Container string
	ReadOnly  bool
}

func startContainer(ctx context.Context, spec containerSpec) (string, error) {
	if spec.Runtime == "" {
		return "", errors.New("aio: no container runtime found (need docker or container CLI)")
	}
	args := []string{"run", "-d", "--name", spec.Name, "-p", fmt.Sprintf("%d:8080", spec.Port)}
	for _, m := range spec.Mounts {
		v := fmt.Sprintf("%s:%s", m.Host, m.Container)
		if m.ReadOnly {
			v += ":ro"
		}
		args = append(args, "-v", v)
	}
	for k, v := range spec.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}
	args = append(args, spec.Image)

	cmd := exec.CommandContext(ctx, string(spec.Runtime), args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("aio: %s run failed: %s: %w", spec.Runtime, strings.TrimSpace(string(out)), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// stopContainer best-effort kills + removes a container.
func stopContainer(rt containerRuntime, idOrName string) error {
	if rt == "" || idOrName == "" {
		return nil
	}
	out, err := exec.Command(string(rt), "rm", "-f", idOrName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("aio: %s rm -f %s: %s: %w", rt, idOrName, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// discoverContainer returns the running container named "<prefix>-<sid>", if any.
func discoverContainer(rt containerRuntime, prefix, sid string) (SandboxInfo, bool) {
	if rt == "" {
		return SandboxInfo{}, false
	}
	name := prefix + "-" + sid
	out, err := exec.Command(string(rt), "inspect", name).Output()
	if err != nil {
		return SandboxInfo{}, false
	}
	port, ok := parseInspectPort(out)
	if !ok {
		return SandboxInfo{}, false
	}
	created, _ := parseInspectCreated(out)
	return SandboxInfo{
		SandboxID:     sid,
		SandboxURL:    fmt.Sprintf("http://localhost:%d", port),
		ContainerName: name,
		ContainerID:   name,
		CreatedAt:     created,
	}, true
}

// listRunningContainers enumerates running containers named with the prefix.
func listRunningContainers(rt containerRuntime, prefix string) []SandboxInfo {
	if rt == "" {
		return nil
	}
	out, err := exec.Command(string(rt), "ps", "--filter", "name="+prefix+"-", "--format", "{{.Names}}").Output()
	if err != nil {
		return nil
	}
	names := strings.Split(strings.TrimSpace(string(out)), "\n")
	var infos []SandboxInfo
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		sid := strings.TrimPrefix(n, prefix+"-")
		if info, ok := discoverContainer(rt, prefix, sid); ok {
			infos = append(infos, info)
		}
	}
	return infos
}

// parseInspectPort returns the first host port mapped to 8080/tcp.
func parseInspectPort(raw []byte) (int, bool) {
	var arr []struct {
		NetworkSettings struct {
			Ports map[string][]struct {
				HostPort string `json:"HostPort"`
			} `json:"Ports"`
		} `json:"NetworkSettings"`
	}
	if err := json.Unmarshal(raw, &arr); err != nil || len(arr) == 0 {
		return 0, false
	}
	bindings, ok := arr[0].NetworkSettings.Ports["8080/tcp"]
	if !ok || len(bindings) == 0 {
		return 0, false
	}
	var port int
	if _, err := fmt.Sscanf(bindings[0].HostPort, "%d", &port); err != nil {
		return 0, false
	}
	return port, true
}

// parseInspectCreated returns the container creation time (zero on error).
func parseInspectCreated(raw []byte) (time.Time, bool) {
	var arr []struct {
		Created string `json:"Created"`
	}
	if err := json.Unmarshal(raw, &arr); err != nil || len(arr) == 0 {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, arr[0].Created)
	if err != nil {
		t, err = time.Parse(time.RFC3339, arr[0].Created)
	}
	return t, err == nil
}

// waitReady polls /v1/ping until it 200s or ctx fires; /v1/ping is the image's only liveness probe.
func waitReady(ctx context.Context, baseURL string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	url := strings.TrimRight(baseURL, "/") + "/v1/ping"
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				return err
			}
			resp, err := client.Do(req)
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode < 300 {
					return nil
				}
			}
		}
	}
}
