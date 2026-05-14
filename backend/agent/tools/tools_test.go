package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
)

// invoke is a small helper: every tool here is built via utils.InferTool
// which returns a tool.InvokableTool; we just need to JSON-marshal a Go map
// then call InvokableRun.
func invoke(t *testing.T, bt tool.BaseTool, args string) string {
	t.Helper()
	it, ok := bt.(tool.InvokableTool)
	if !ok {
		t.Fatalf("tool is not InvokableTool")
	}
	out, err := it.InvokableRun(context.Background(), args)
	if err != nil {
		t.Fatalf("tool invoke failed: %v", err)
	}
	return out
}

// invokeExpectErr returns the error message; used for negative cases.
func invokeExpectErr(t *testing.T, bt tool.BaseTool, args string) error {
	t.Helper()
	it := bt.(tool.InvokableTool)
	_, err := it.InvokableRun(context.Background(), args)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	return err
}

func TestLs(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bt, err := GetLsTool(root)
	if err != nil {
		t.Fatal(err)
	}
	got := invoke(t, bt, `{"path":"."}`)
	// os.ReadDir sorts by name → "a.txt\nb.txt".
	if got != "a.txt\nb.txt" {
		t.Fatalf("ls output mismatch:\ngot:  %q\nwant: %q", got, "a.txt\nb.txt")
	}

	emptyDir := t.TempDir()
	bt2, _ := GetLsTool(emptyDir)
	if got := invoke(t, bt2, `{"path":"."}`); got != noFilesFound {
		t.Fatalf("empty dir: got %q want %q", got, noFilesFound)
	}
}

func TestReadFile(t *testing.T) {
	root := t.TempDir()
	body := "line1\nline2\nline3"
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	bt, _ := GetReadFileTool(root)

	got := invoke(t, bt, `{"file_path":"f.txt"}`)
	want := "     1\tline1\n     2\tline2\n     3\tline3"
	if got != want {
		t.Fatalf("read_file default output:\ngot:  %q\nwant: %q", got, want)
	}

	// offset + limit slice in the middle (1-based offset).
	got = invoke(t, bt, `{"file_path":"f.txt","offset":2,"limit":1}`)
	want = "     2\tline2"
	if got != want {
		t.Fatalf("read_file offset/limit:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestWriteFile(t *testing.T) {
	root := t.TempDir()
	bt, _ := GetWriteFileTool(root)

	got := invoke(t, bt, `{"file_path":"sub/new.txt","content":"hello"}`)
	if got != "Updated file sub/new.txt" {
		t.Fatalf("write_file ack:\ngot:  %q\nwant: %q", got, "Updated file sub/new.txt")
	}
	data, err := os.ReadFile(filepath.Join(root, "sub", "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("write_file content: got %q want %q", string(data), "hello")
	}
}

func TestEditFile(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "f.txt")
	if err := os.WriteFile(p, []byte("foo bar foo"), 0o644); err != nil {
		t.Fatal(err)
	}
	bt, _ := GetEditFileTool(root)

	// 2 occurrences without replace_all → ambiguity error.
	err := invokeExpectErr(t, bt, `{"file_path":"f.txt","old_string":"foo","new_string":"baz"}`)
	if !strings.Contains(err.Error(), "appears 2 times") {
		t.Fatalf("expected ambiguity error, got %v", err)
	}

	// replace_all=true succeeds.
	got := invoke(t, bt, `{"file_path":"f.txt","old_string":"foo","new_string":"baz","replace_all":true}`)
	if got != "Successfully replaced the string in 'f.txt'" {
		t.Fatalf("edit_file ack:\ngot:  %q", got)
	}
	data, _ := os.ReadFile(p)
	if string(data) != "baz bar baz" {
		t.Fatalf("edit_file content: got %q", string(data))
	}
}

func TestGlob(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.go", "b.go", "c.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bt, _ := GetGlobTool(root)
	got := invoke(t, bt, `{"pattern":"*.go","path":""}`)
	// filepath.Glob returns alphabetical paths; relative to root.
	if got != "a.go\nb.go" {
		t.Fatalf("glob:\ngot:  %q\nwant: %q", got, "a.go\nb.go")
	}

	if got := invoke(t, bt, `{"pattern":"*.rs","path":""}`); got != noFilesFound {
		t.Fatalf("no match: got %q want %q", got, noFilesFound)
	}
}

func TestGlobDefaultsToRecursiveSearch(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "yaml", "CHANGELOG.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	bt, _ := GetGlobTool(root)

	got := invoke(t, bt, `{"pattern":"CHANGELOG.md","path":""}`)
	if got != "yaml/CHANGELOG.md" {
		t.Fatalf("recursive glob:\ngot:  %q\nwant: %q", got, "yaml/CHANGELOG.md")
	}
}

func TestGrepFilesWithMatches(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("nothing here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bt, _ := GetGrepTool(root)

	// Default output_mode is files_with_matches; "Found N file" header.
	got := invoke(t, bt, `{"pattern":"hello"}`)
	want := "Found 1 file\na.txt"
	if got != want {
		t.Fatalf("grep files:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestGrepContent(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bt, _ := GetGrepTool(root)

	got := invoke(t, bt, `{"pattern":"hello","output_mode":"content"}`)
	want := "a.txt:1:hello"
	if got != want {
		t.Fatalf("grep content:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestGrepCount(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("x\nx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bt, _ := GetGrepTool(root)

	got := invoke(t, bt, `{"pattern":"x","output_mode":"count"}`)
	if !strings.Contains(got, "a.txt:2") {
		t.Fatalf("grep count missing per-file entry: %q", got)
	}
	if !strings.Contains(got, "Found 2 total occurrences across 1 file.") {
		t.Fatalf("grep count summary mismatch: %q", got)
	}
}

func TestExecute(t *testing.T) {
	root := t.TempDir()
	bt, _ := GetExecuteTool(root)

	got := invoke(t, bt, `{"command":"echo hi"}`)
	if !strings.HasPrefix(got, "hi") {
		t.Fatalf("execute echo: %q", got)
	}

	got = invoke(t, bt, `{"command":"exit 3"}`)
	if !strings.Contains(got, "[Command failed with exit code 3]") {
		t.Fatalf("execute non-zero exit: %q", got)
	}

	got = invoke(t, bt, `{"command":"true"}`)
	if got != "[Command executed successfully with no output]" {
		t.Fatalf("execute empty success: %q", got)
	}
}

func TestAskClarificationTool(t *testing.T) {
	bt, err := GetAskClarificationTool()
	if err != nil {
		t.Fatal(err)
	}
	info, err := bt.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != askClarificationToolName {
		t.Fatalf("name: got %q want %q", info.Name, askClarificationToolName)
	}
	schema, err := info.ParamsOneOf.ToJSONSchema()
	if err != nil {
		t.Fatalf("ToJSONSchema: %v", err)
	}
	for _, name := range []string{"question", "clarification_type", "context", "options"} {
		if _, ok := schema.Properties.Get(name); !ok {
			t.Fatalf("schema missing %q", name)
		}
	}
	for _, name := range []string{"question", "clarification_type"} {
		if !containsString(schema.Required, name) {
			t.Fatalf("schema required missing %q: %v", name, schema.Required)
		}
	}

	got := invoke(t, bt, `{"question":"Which environment?","clarification_type":"approach_choice","options":["dev","prod"]}`)
	if got != "Clarification request processed by middleware" {
		t.Fatalf("clarification fallback output: %q", got)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestBuildBuiltinToolsCount(t *testing.T) {
	got := BuildBuiltinTools(t.TempDir())
	if len(got) != 8 {
		t.Fatalf("BuildBuiltinTools: got %d tools, want 8", len(got))
	}
	// Names should match eino's expected wire identifiers exactly.
	want := []string{"ask_clarification", "ls", "read_file", "write_file", "edit_file", "glob", "grep", "execute"}
	for i, bt := range got {
		info, err := bt.Info(context.Background())
		if err != nil {
			t.Fatalf("tool[%d].Info: %v", i, err)
		}
		if info.Name != want[i] {
			t.Fatalf("tool[%d] name: got %q want %q", i, info.Name, want[i])
		}
	}
}
