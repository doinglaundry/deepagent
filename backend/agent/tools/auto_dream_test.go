package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAutoDreamWriteFileStaysInsideMemoryRoot(t *testing.T) {
	memoryRoot := filepath.Join(t.TempDir(), "dream-memory")
	bt, err := GetAutoDreamWriteFileTool(memoryRoot)
	if err != nil {
		t.Fatal(err)
	}

	invoke(t, bt, `{"file_path":"MEMORY.md","content":"hello"}`)
	data, err := os.ReadFile(filepath.Join(memoryRoot, "MEMORY.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("memory content = %q, want hello", string(data))
	}

	err = invokeExpectErr(t, bt, `{"file_path":"../outside.md","content":"bad"}`)
	if !strings.Contains(err.Error(), "only write inside") {
		t.Fatalf("unexpected escape error: %v", err)
	}
}

func TestAutoDreamReadOnlyShellFilter(t *testing.T) {
	if !isReadOnlyShellCommand("rg auto-dream backend") {
		t.Fatal("rg should be allowed")
	}
	for _, command := range []string{"rm -rf x", "ls > out.txt", "grep x y | wc -l", "pwd; rm x"} {
		if isReadOnlyShellCommand(command) {
			t.Fatalf("command should be denied: %s", command)
		}
	}
}
