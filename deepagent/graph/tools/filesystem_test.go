package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	filesystempkg "eino-cli/deepagent/graph/filesystem"
	agentmodel "eino-cli/deepagent/model"

	einotool "github.com/cloudwego/eino/components/tool"
)

func TestTools_AllRegisteredNamesSchemasAndArgumentAliases(t *testing.T) {
	ctx := context.Background()
	filesystem, err := filesystempkg.NewLocalFilesystem(&filesystempkg.LocalFilesystemConfig{RootDir: t.TempDir(), VirtualMode: true}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer filesystem.Close(ctx)
	toolsByName := map[string]einotool.InvokableTool{}
	descriptorsByName := map[string]agentmodel.ToolDescriptor{}
	toolDescriptors, err := NewFilesystemTools(filesystem, FilesystemToolOptions{EnableCommands: true, EnablePatch: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, toolDescriptor := range toolDescriptors {
		toolInfo, err := toolDescriptor.Tool.Info(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if toolInfo == nil || toolInfo.Name == "" || toolInfo.ParamsOneOf == nil {
			t.Fatalf("tool has incomplete schema: %+v", toolInfo)
		}
		if toolsByName[toolInfo.Name] != nil {
			t.Fatalf("duplicate tool name %q", toolInfo.Name)
		}
		toolsByName[toolInfo.Name] = toolDescriptor.Tool.(einotool.InvokableTool)
		descriptorsByName[toolInfo.Name] = toolDescriptor
	}
	for _, name := range []string{"list_files", "read_file", "write_file", "edit_file", "delete_file", "glob", "grep", "rg", "semantic_search", "read_lints", "apply_patch", "execute", "shell", "await_shell"} {
		if toolsByName[name] == nil {
			t.Fatalf("missing %s", name)
		}
	}
	for _, name := range []string{"write_file", "edit_file", "delete_file", "apply_patch", "read_lints", "execute", "shell"} {
		if !descriptorsByName[name].RequiresApproval || descriptorsByName[name].ReadOnly || descriptorsByName[name].ParallelSafe {
			t.Fatalf("%s must execute serially with approval", name)
		}
	}
	toolSet, err := NewToolSet(ctx, toolDescriptors)
	if err != nil {
		t.Fatal(err)
	}
	readOnlyToolSet, err := toolSet.FilterTools(ctx, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	toolInfos, err := readOnlyToolSet.GetToolInfos(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var toolNames []string
	for _, toolInfo := range toolInfos {
		toolNames = append(toolNames, toolInfo.Name)
	}
	expectedToolNames := []string{"list_files", "read_file", "glob", "grep", "rg", "semantic_search", "await_shell"}
	if !reflect.DeepEqual(toolNames, expectedToolNames) {
		t.Fatalf("read-only model tools = %v, want %v", toolNames, expectedToolNames)
	}
	for _, step := range []struct{ name, args string }{
		{"write_file", `{"path":"a.txt","content":"first\nsecond\n"}`},
		{"edit_file", `{"path":"a.txt","old":"second","new":"changed"}`},
	} {
		_, err := toolsByName[step.name].InvokableRun(ctx, step.args)
		if err != nil {
			t.Fatal(err)
		}
	}
	fileContent, err := toolsByName["read_file"].InvokableRun(ctx, `{"path":"a.txt","offset":2,"limit":1}`)
	if err != nil || !strings.Contains(fileContent, "changed") || strings.Contains(fileContent, "first") {
		t.Fatalf("read=%q err=%v", fileContent, err)
	}
	_, invokableRunErr := toolsByName["delete_file"].InvokableRun(ctx, `{"path":"a.txt"}`)
	if invokableRunErr != nil {
		t.Fatal(invokableRunErr)
	}
}

func TestFilesystemPreservesWorkerReadAndExactEditContracts(t *testing.T) {
	ctx := context.Background()
	workspaceRoot := t.TempDir()
	filesystem := newTestLocalFilesystem(t, &filesystempkg.LocalFilesystemConfig{RootDir: workspaceRoot, VirtualMode: true, MaxFileSizeMB: 1})
	readFileTool := NewReadFileTool(filesystem).Tool.(einotool.InvokableTool)
	editFileTool := NewEditFileTool(filesystem).Tool.(einotool.InvokableTool)
	initialWriteErr := os.WriteFile(filepath.Join(workspaceRoot, "data.txt"), []byte("hello world"), 0600)
	if initialWriteErr != nil {
		t.Fatal(initialWriteErr)
	}
	_, replaceWorldErr := editFileTool.InvokableRun(ctx, `{"path":"data.txt","old":"world","new":"Go"}`)
	if replaceWorldErr != nil {
		t.Fatal(replaceWorldErr)
	}
	editedContent, readEditedFileErr := readFileTool.InvokableRun(ctx, `{"path":"data.txt"}`)
	if readEditedFileErr != nil || editedContent != "hello Go" {
		t.Fatalf("read=%q err=%v", editedContent, readEditedFileErr)
	}
	for _, oldText := range []string{"missing", ""} {
		_, err := editFileTool.InvokableRun(ctx, `{"path":"data.txt","old":"`+oldText+`","new":"bad"}`)
		if err == nil {
			t.Fatalf("invalid edit accepted: old=%q", oldText)
		}
	}
	duplicateWriteErr := os.WriteFile(filepath.Join(workspaceRoot, "data.txt"), []byte("twice twice"), 0600)
	if duplicateWriteErr != nil {
		t.Fatal(duplicateWriteErr)
	}
	_, ambiguousEditErr := editFileTool.InvokableRun(ctx, `{"path":"data.txt","old":"twice","new":"bad"}`)
	if ambiguousEditErr == nil {
		t.Fatal("ambiguous edit accepted")
	}
	unchangedContent, readUnchangedFileErr := readFileTool.InvokableRun(ctx, `{"path":"data.txt"}`)
	if readUnchangedFileErr != nil || unchangedContent != "twice twice" {
		t.Fatalf("rejected edit changed file: %q %v", unchangedContent, readUnchangedFileErr)
	}
	largeFileWriteErr := os.WriteFile(filepath.Join(workspaceRoot, "large.txt"), []byte(strings.Repeat("x", (1<<20)+1)), 0600)
	if largeFileWriteErr != nil {
		t.Fatal(largeFileWriteErr)
	}
	_, readLargeFileErr := readFileTool.InvokableRun(ctx, `{"path":"large.txt"}`)
	if readLargeFileErr == nil {
		t.Fatal("oversized file accepted")
	}
}

func TestFilesystemToolArgumentPresenceAndReplaceAll(t *testing.T) {
	ctx := context.Background()
	workspaceRoot := t.TempDir()
	filesystem := newTestLocalFilesystem(t, &filesystempkg.LocalFilesystemConfig{RootDir: workspaceRoot, VirtualMode: true})
	writeFileTool := NewWriteFileTool(filesystem).Tool.(einotool.InvokableTool)
	editFileTool := NewEditFileTool(filesystem).Tool.(einotool.InvokableTool)
	readFileTool := NewReadFileTool(filesystem).Tool.(einotool.InvokableTool)
	err := os.WriteFile(filepath.Join(workspaceRoot, "data.txt"), []byte("twice twice"), 0600)
	if err != nil {
		t.Fatal(err)
	}

	for _, arguments := range []string{
		`{"path":"data.txt"}`,
		`{"path":"data.txt","content":null}`,
	} {
		_, err = writeFileTool.InvokableRun(ctx, arguments)
		if err == nil {
			t.Fatalf("accepted write arguments: %s", arguments)
		}
	}
	fileContent, err := readFileTool.InvokableRun(ctx, `{"path":"data.txt"}`)
	if err != nil || fileContent != "twice twice" {
		t.Fatalf("invalid write changed file: %q %v", fileContent, err)
	}

	for _, arguments := range []string{
		`{"path":"data.txt","old":"twice"}`,
		`{"path":"data.txt","old":"twice","new":null}`,
	} {
		_, err = editFileTool.InvokableRun(ctx, arguments)
		if err == nil {
			t.Fatalf("accepted edit arguments: %s", arguments)
		}
	}
	fileContent, err = readFileTool.InvokableRun(ctx, `{"path":"data.txt"}`)
	if err != nil || fileContent != "twice twice" {
		t.Fatalf("invalid edit changed file: %q %v", fileContent, err)
	}

	_, err = writeFileTool.InvokableRun(ctx, `{"path":"empty.txt","content":""}`)
	if err != nil {
		t.Fatal(err)
	}
	fileContent, err = readFileTool.InvokableRun(ctx, `{"path":"empty.txt"}`)
	if err != nil || fileContent != "" {
		t.Fatalf("explicit empty write failed: %q %v", fileContent, err)
	}

	err = os.WriteFile(filepath.Join(workspaceRoot, "remove.txt"), []byte("remove me"), 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = editFileTool.InvokableRun(ctx, `{"path":"remove.txt","old":"remove","new":""}`)
	if err != nil {
		t.Fatal(err)
	}
	fileContent, err = readFileTool.InvokableRun(ctx, `{"path":"remove.txt"}`)
	if err != nil || fileContent != " me" {
		t.Fatalf("explicit empty edit failed: %q %v", fileContent, err)
	}

	_, err = editFileTool.InvokableRun(ctx, `{"path":"data.txt","old":"twice","new":"once","replace_all":false}`)
	if err == nil {
		t.Fatal("replace_all=false accepted ambiguous edit")
	}
	fileContent, err = readFileTool.InvokableRun(ctx, `{"path":"data.txt"}`)
	if err != nil || fileContent != "twice twice" {
		t.Fatalf("ambiguous edit changed file: %q %v", fileContent, err)
	}
	_, err = editFileTool.InvokableRun(ctx, `{"path":"data.txt","old":"twice","new":"once","replace_all":true}`)
	if err != nil {
		t.Fatal(err)
	}
	fileContent, err = readFileTool.InvokableRun(ctx, `{"path":"data.txt"}`)
	if err != nil || fileContent != "once once" {
		t.Fatalf("replace_all edit failed: %q %v", fileContent, err)
	}

	toolInfo, err := editFileTool.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	encodedToolInfo, err := json.Marshal(toolInfo)
	if err != nil {
		t.Fatal(err)
	}
	var toolSchema struct {
		Params map[string]struct {
			Type     string `json:"Type"`
			Required bool   `json:"Required"`
		} `json:"params"`
	}
	err = json.Unmarshal(encodedToolInfo, &toolSchema)
	if err != nil {
		t.Fatal(err)
	}
	replaceAllParameter, ok := toolSchema.Params["replace_all"]
	if !ok || replaceAllParameter.Type != "boolean" || replaceAllParameter.Required {
		t.Fatalf("replace_all schema = %+v", replaceAllParameter)
	}
}

type pathValidationFilesystem struct {
	agentmodel.Filesystem
	calls int
}

func (filesystem *pathValidationFilesystem) Write(context.Context, string, string) (*agentmodel.WriteResult, error) {
	filesystem.calls++
	return &agentmodel.WriteResult{}, nil
}

func (filesystem *pathValidationFilesystem) Edit(context.Context, string, string, string, bool) (*agentmodel.EditResult, error) {
	filesystem.calls++
	return &agentmodel.EditResult{}, nil
}

func TestFileMutationRequiresPathBeforeBackendInvocation(t *testing.T) {
	filesystem := &pathValidationFilesystem{}
	for _, toolDescriptor := range []agentmodel.ToolDescriptor{NewWriteFileTool(filesystem), NewEditFileTool(filesystem)} {
		toolInfo, err := toolDescriptor.Tool.Info(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, pathArgument := range []string{"", `,"path":null`, `,"path":""`, `,"path":"  "`} {
			t.Run(toolInfo.Name+pathArgument, func(t *testing.T) {
				arguments := `{"content":"value","old":"old","new":"new"` + pathArgument + `}`
				_, err := toolDescriptor.Tool.(einotool.InvokableTool).InvokableRun(context.Background(), arguments)
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

type dockerToolProvider struct{ agentmodel.Sandbox }

func (*dockerToolProvider) GetDockerExecTarget() (string, bool) { return "test-container", true }

func (*dockerToolProvider) ResolveContainerPath(_ context.Context, path string) (string, error) {
	return path, nil
}

func TestWorkspaceToolSchemasMatchLocalAndDocker(t *testing.T) {
	ctx := context.Background()
	localFilesystem, err := filesystempkg.NewLocalFilesystem(&filesystempkg.LocalFilesystemConfig{RootDir: t.TempDir(), VirtualMode: true}, "local")
	if err != nil {
		t.Fatal(err)
	}
	defer localFilesystem.Close(ctx)
	dockerFilesystem, err := filesystempkg.NewDockerFilesystem(&dockerToolProvider{}, "/workspace", "docker", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dockerFilesystem.Close(ctx)
	for _, options := range []FilesystemToolOptions{{ReadOnly: true}, {EnableCommands: true, EnablePatch: true}} {
		toolSchemas := make([]map[string]any, 0, 2)
		for _, filesystem := range []agentmodel.ToolFilesystem{localFilesystem, dockerFilesystem} {
			toolDescriptors, err := NewFilesystemTools(filesystem, options)
			if err != nil {
				t.Fatal(err)
			}
			toolsByName := map[string]any{}
			for _, toolDescriptor := range toolDescriptors {
				toolInfo, err := toolDescriptor.Tool.Info(ctx)
				if err != nil {
					t.Fatal(err)
				}
				_, exists := toolsByName[toolInfo.Name]
				if exists {
					t.Fatalf("duplicate tool %s", toolInfo.Name)
				}
				toolsByName[toolInfo.Name] = toolInfo
			}
			toolSchemas = append(toolSchemas, toolsByName)
		}
		if !reflect.DeepEqual(toolSchemas[0], toolSchemas[1]) {
			t.Fatalf("local and Docker tool schemas differ: %v / %v", toolSchemas[0], toolSchemas[1])
		}
	}
}

type applyPatchProbe struct {
	*filesystempkg.LocalFilesystem
	patch string
}

func (patchProbe *applyPatchProbe) ApplyPatch(_ context.Context, patch string) (string, error) {
	patchProbe.patch = patch
	return "patched", nil
}

func TestReadOnlyFilesystemOmitsMutations(t *testing.T) {
	filesystem := newTestLocalFilesystem(t, &filesystempkg.LocalFilesystemConfig{RootDir: t.TempDir(), VirtualMode: true})
	toolDescriptors, err := NewFilesystemTools(filesystem, FilesystemToolOptions{ReadOnly: true, EnableCommands: true, EnablePatch: true})
	if err != nil {
		t.Fatal(err)
	}
	var toolNames []string
	for _, toolDescriptor := range toolDescriptors {
		toolInfo, err := toolDescriptor.Tool.Info(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		descriptor := toolDescriptor
		if !descriptor.ReadOnly || descriptor.RequiresApproval {
			t.Fatalf("read-only factory exposed mutation: %s", toolInfo.Name)
		}
		toolNames = append(toolNames, toolInfo.Name)
	}
	expectedToolNames := []string{"list_files", "read_file", "glob", "grep", "rg", "semantic_search"}
	if !reflect.DeepEqual(toolNames, expectedToolNames) {
		t.Fatalf("read-only tools = %v, want %v", toolNames, expectedToolNames)
	}
}

func TestApplyPatchDelegatesToFilesystem(t *testing.T) {
	filesystem := newTestLocalFilesystem(t, &filesystempkg.LocalFilesystemConfig{RootDir: t.TempDir(), VirtualMode: true})
	patchProbe := &applyPatchProbe{LocalFilesystem: filesystem}
	applyPatchTool := NewApplyPatchTool(patchProbe).Tool.(einotool.InvokableTool)
	output, err := applyPatchTool.InvokableRun(context.Background(), `{"patch":"*** Begin Patch\\n*** End Patch"}`)
	if err != nil || output != "patched" || patchProbe.patch == "" {
		t.Fatalf("apply_patch = %q, %v, patch=%q", output, err, patchProbe.patch)
	}
}

func TestDeleteFileRefusesDirectories(t *testing.T) {
	workspaceRoot := t.TempDir()
	filesystem := newTestLocalFilesystem(t, &filesystempkg.LocalFilesystemConfig{RootDir: workspaceRoot, VirtualMode: true})
	deleteFileTool := NewDeleteFileTool(filesystem).Tool.(einotool.InvokableTool)
	_, err := deleteFileTool.InvokableRun(context.Background(), `{"path":"."}`)
	if err == nil {
		t.Fatal("delete_file accepted a directory")
	}
	filePath := filepath.Join(workspaceRoot, "remove.txt")
	err = os.WriteFile(filePath, []byte("x"), 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = deleteFileTool.InvokableRun(context.Background(), `{"path":"remove.txt"}`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = os.Stat(filePath)
	if !os.IsNotExist(err) {
		t.Fatalf("file still exists: %v", err)
	}
}
