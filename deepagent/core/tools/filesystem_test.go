package tools

import (
	"context"
	"eino-cli/deepagent/core/backend"
	"eino-cli/deepagent/sandbox"
	"encoding/json"
	einotool "github.com/cloudwego/eino/components/tool"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestTools_AllRegisteredNamesSchemasAndArgumentAliases(t *testing.T) {
	ctx := context.Background()
	b, err := backend.NewLocalFilesystem(&backend.LocalFilesystemConfig{RootDir: t.TempDir(), VirtualMode: true}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close(ctx)
	registered := map[string]einotool.InvokableTool{}
	items, err := NewFilesystemTools(b, FilesystemToolOptions{EnableCommands: true, EnablePatch: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range items {
		info, err := tool.Info(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if info == nil || info.Name == "" || info.ParamsOneOf == nil {
			t.Fatalf("tool has incomplete schema: %+v", info)
		}
		if registered[info.Name] != nil {
			t.Fatalf("duplicate tool name %q", info.Name)
		}
		registered[info.Name] = tool.(einotool.InvokableTool)
	}
	for _, name := range []string{"list_files", "read_file", "write_file", "edit_file", "delete_file", "glob", "grep", "rg", "semantic_search", "read_lints", "apply_patch", "execute", "shell", "await_shell"} {
		if registered[name] == nil {
			t.Fatalf("missing %s", name)
		}
	}
	for _, name := range []string{"write_file", "edit_file", "delete_file"} {
		approval, ok := registered[name].(interface{ RequiresApproval() bool })
		if !ok || !approval.RequiresApproval() {
			t.Fatalf("%s must request approval", name)
		}
	}
	for _, step := range []struct{ name, args string }{
		{"write_file", `{"path":"a.txt","content":"first\nsecond\n"}`},
		{"edit_file", `{"path":"a.txt","old":"second","new":"changed"}`},
	} {
		if _, err := registered[step.name].InvokableRun(ctx, step.args); err != nil {
			t.Fatal(err)
		}
	}
	text, err := registered["read_file"].InvokableRun(ctx, `{"path":"a.txt","offset":2,"limit":1}`)
	if err != nil || !strings.Contains(text, "changed") || strings.Contains(text, "first") {
		t.Fatalf("read=%q err=%v", text, err)
	}
	if _, err := registered["delete_file"].InvokableRun(ctx, `{"path":"a.txt"}`); err != nil {
		t.Fatal(err)
	}
}

func TestFilesystemPreservesWorkerReadAndExactEditContracts(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	b := mustLocalFilesystem(t, &backend.LocalFilesystemConfig{RootDir: root, VirtualMode: true, MaxFileSizeMB: 1})
	read := NewReadFileTool(b).(einotool.InvokableTool)
	edit := NewEditFileTool(b).(einotool.InvokableTool)
	if err := os.WriteFile(filepath.Join(root, "data.txt"), []byte("hello world"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := edit.InvokableRun(ctx, `{"path":"data.txt","old":"world","new":"Go"}`); err != nil {
		t.Fatal(err)
	}
	if result, err := read.InvokableRun(ctx, `{"path":"data.txt"}`); err != nil || result != "hello Go" {
		t.Fatalf("read=%q err=%v", result, err)
	}
	for _, old := range []string{"missing", ""} {
		if _, err := edit.InvokableRun(ctx, `{"path":"data.txt","old":"`+old+`","new":"bad"}`); err == nil {
			t.Fatalf("invalid edit accepted: old=%q", old)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "data.txt"), []byte("twice twice"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := edit.InvokableRun(ctx, `{"path":"data.txt","old":"twice","new":"bad"}`); err == nil {
		t.Fatal("ambiguous edit accepted")
	}
	if result, err := read.InvokableRun(ctx, `{"path":"data.txt"}`); err != nil || result != "twice twice" {
		t.Fatalf("rejected edit changed file: %q %v", result, err)
	}
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(strings.Repeat("x", (1<<20)+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := read.InvokableRun(ctx, `{"path":"large.txt"}`); err == nil {
		t.Fatal("oversized file accepted")
	}
}

func TestFilesystemToolArgumentPresenceAndReplaceAll(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	b := mustLocalFilesystem(t, &backend.LocalFilesystemConfig{RootDir: root, VirtualMode: true})
	write := NewWriteFileTool(b).(einotool.InvokableTool)
	edit := NewEditFileTool(b).(einotool.InvokableTool)
	read := NewReadFileTool(b).(einotool.InvokableTool)
	err := os.WriteFile(filepath.Join(root, "data.txt"), []byte("twice twice"), 0600)
	if err != nil {
		t.Fatal(err)
	}

	for _, args := range []string{
		`{"path":"data.txt"}`,
		`{"path":"data.txt","content":null}`,
	} {
		_, err = write.InvokableRun(ctx, args)
		if err == nil {
			t.Fatalf("accepted write arguments: %s", args)
		}
	}
	result, err := read.InvokableRun(ctx, `{"path":"data.txt"}`)
	if err != nil || result != "twice twice" {
		t.Fatalf("invalid write changed file: %q %v", result, err)
	}

	for _, args := range []string{
		`{"path":"data.txt","old":"twice"}`,
		`{"path":"data.txt","old":"twice","new":null}`,
	} {
		_, err = edit.InvokableRun(ctx, args)
		if err == nil {
			t.Fatalf("accepted edit arguments: %s", args)
		}
	}
	result, err = read.InvokableRun(ctx, `{"path":"data.txt"}`)
	if err != nil || result != "twice twice" {
		t.Fatalf("invalid edit changed file: %q %v", result, err)
	}

	_, err = write.InvokableRun(ctx, `{"path":"empty.txt","content":""}`)
	if err != nil {
		t.Fatal(err)
	}
	result, err = read.InvokableRun(ctx, `{"path":"empty.txt"}`)
	if err != nil || result != "" {
		t.Fatalf("explicit empty write failed: %q %v", result, err)
	}

	err = os.WriteFile(filepath.Join(root, "remove.txt"), []byte("remove me"), 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = edit.InvokableRun(ctx, `{"path":"remove.txt","old":"remove","new":""}`)
	if err != nil {
		t.Fatal(err)
	}
	result, err = read.InvokableRun(ctx, `{"path":"remove.txt"}`)
	if err != nil || result != " me" {
		t.Fatalf("explicit empty edit failed: %q %v", result, err)
	}

	_, err = edit.InvokableRun(ctx, `{"path":"data.txt","old":"twice","new":"once","replace_all":false}`)
	if err == nil {
		t.Fatal("replace_all=false accepted ambiguous edit")
	}
	result, err = read.InvokableRun(ctx, `{"path":"data.txt"}`)
	if err != nil || result != "twice twice" {
		t.Fatalf("ambiguous edit changed file: %q %v", result, err)
	}
	_, err = edit.InvokableRun(ctx, `{"path":"data.txt","old":"twice","new":"once","replace_all":true}`)
	if err != nil {
		t.Fatal(err)
	}
	result, err = read.InvokableRun(ctx, `{"path":"data.txt"}`)
	if err != nil || result != "once once" {
		t.Fatalf("replace_all edit failed: %q %v", result, err)
	}

	info, err := edit.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	var encoded struct {
		Params map[string]struct {
			Type     string `json:"Type"`
			Required bool   `json:"Required"`
		} `json:"params"`
	}
	err = json.Unmarshal(data, &encoded)
	if err != nil {
		t.Fatal(err)
	}
	replaceAll, ok := encoded.Params["replace_all"]
	if !ok || replaceAll.Type != "boolean" || replaceAll.Required {
		t.Fatalf("replace_all schema = %+v", replaceAll)
	}
}

type pathValidationFilesystem struct {
	backend.Filesystem
	calls int
}

func (f *pathValidationFilesystem) Write(context.Context, string, string) (*backend.WriteResult, error) {
	f.calls++
	return &backend.WriteResult{}, nil
}

func (f *pathValidationFilesystem) Edit(context.Context, string, string, string, bool) (*backend.EditResult, error) {
	f.calls++
	return &backend.EditResult{}, nil
}

func TestFileMutationRequiresPathBeforeBackendInvocation(t *testing.T) {
	filesystem := &pathValidationFilesystem{}
	for _, item := range []einotool.BaseTool{NewWriteFileTool(filesystem), NewEditFileTool(filesystem)} {
		info, err := item.Info(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"", `,"path":null`, `,"path":""`, `,"path":"  "`} {
			t.Run(info.Name+path, func(t *testing.T) {
				args := `{"content":"value","old":"old","new":"new"` + path + `}`
				_, err := item.(einotool.InvokableTool).InvokableRun(context.Background(), args)
				if err == nil || err.Error() != "path is required" {
					t.Fatalf("path validation error = %v", err)
				}
			})
		}
	}
	if filesystem.calls != 0 {
		t.Fatalf("invalid paths reached backend %d times", filesystem.calls)
	}
}

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
