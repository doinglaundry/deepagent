package filesystem

import (
	"context"
	"github.com/cloudwego/eino/components/tool"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRootRejectsTraversalAndSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0600)
	os.Symlink(outside, filepath.Join(root, "link"))
	fs, e := NewFilesystem(root)
	if e != nil {
		t.Fatal(e)
	}
	var read tool.InvokableTool
	for _, tt := range fs.Tools() {
		i, _ := tt.Info(context.Background())
		if i.Name == "read_file" {
			read = tt.(tool.InvokableTool)
		}
	}
	for _, arg := range []string{`{"path":"../secret"}`, `{"path":"link/secret"}`} {
		if _, e = read.InvokableRun(context.Background(), arg); e == nil {
			t.Fatalf("escaped root: %s", arg)
		}
	}
}
func TestWriteAndReadAndApprovalMetadata(t *testing.T) {
	fs, _ := NewFilesystem(t.TempDir())
	var read, write tool.InvokableTool
	for _, tt := range fs.Tools() {
		i, _ := tt.Info(context.Background())
		switch i.Name {
		case "read_file":
			read = tt.(tool.InvokableTool)
		case "write_file":
			write = tt.(tool.InvokableTool)
			if !tt.(interface{ RequiresApproval() bool }).RequiresApproval() {
				t.Fatal("write lacks approval gate")
			}
		case "execute":
			if tt.(interface{ ReadOnly() bool }).ReadOnly() {
				t.Fatal("command incorrectly permitted in plan mode")
			}
		}
	}
	if _, e := write.InvokableRun(context.Background(), `{"path":"x.txt","content":"hello"}`); e != nil {
		t.Fatal(e)
	}
	v, e := read.InvokableRun(context.Background(), `{"path":"x.txt"}`)
	if e != nil || v != "hello" {
		t.Fatal(v, e)
	}
}

func TestRootedSearchAndUnambiguousEdit(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello world"), 0600)
	fs, _ := NewFilesystem(root)
	ts := map[string]tool.InvokableTool{}
	for _, tt := range fs.Tools() {
		info, _ := tt.Info(context.Background())
		if it, ok := tt.(tool.InvokableTool); ok {
			ts[info.Name] = it
		}
	}
	if ts["search_files"] == nil || ts["edit_file"] == nil {
		t.Fatal("search/edit unavailable")
	}
	out, e := ts["search_files"].InvokableRun(context.Background(), `{"path":".","query":"hello"}`)
	if e != nil || !strings.Contains(out, "a.txt:1:hello world") {
		t.Fatal(out, e)
	}
	if _, e = ts["edit_file"].InvokableRun(context.Background(), `{"path":"a.txt","old":"world","new":"Go"}`); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	if string(b) != "hello Go" {
		t.Fatal(string(b))
	}
	if _, e = ts["edit_file"].InvokableRun(context.Background(), `{"path":"a.txt","old":"missing","new":"oops"}`); e == nil {
		t.Fatal("ambiguous/missing edit succeeded")
	}
}
