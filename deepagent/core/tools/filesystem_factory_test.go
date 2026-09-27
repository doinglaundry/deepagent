package tools

import (
	"context"
	"reflect"
	"testing"

	"eino-cli/deepagent/core/backend"
	"eino-cli/deepagent/sandbox"
)

type dockerToolProvider struct{ sandbox.Sandbox }

func (*dockerToolProvider) DockerExecTarget() (string, bool) { return "test-container", true }
func (*dockerToolProvider) ResolveContainerPath(_ context.Context, p string) (string, error) {
	return p, nil
}

func TestWorkspaceToolSchemasMatchLocalAndDocker(t *testing.T) {
	ctx := context.Background()
	local, err := backend.NewLocalFilesystem(&backend.LocalFilesystemConfig{RootDir: t.TempDir(), VirtualMode: true}, "local")
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close(ctx)
	docker, err := backend.NewDockerFilesystem(&dockerToolProvider{}, "/workspace", "docker")
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close(ctx)
	for _, opts := range []FilesystemToolOptions{{ReadOnly: true}, {EnableCommands: true, EnablePatch: true}} {
		infos := make([]map[string]any, 0, 2)
		for _, ws := range []backend.ToolFilesystem{local, docker} {
			items, err := NewFilesystemTools(ws, opts)
			if err != nil {
				t.Fatal(err)
			}
			byName := map[string]any{}
			for _, item := range items {
				info, err := item.Info(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if _, exists := byName[info.Name]; exists {
					t.Fatalf("duplicate tool %s", info.Name)
				}
				byName[info.Name] = info
			}
			infos = append(infos, byName)
		}
		if !reflect.DeepEqual(infos[0], infos[1]) {
			t.Fatalf("local and Docker tool schemas differ: %v / %v", infos[0], infos[1])
		}
	}
}
