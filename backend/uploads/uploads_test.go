package uploads

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eino-cli/backend/config"
)

func setTestRoot(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	cleanup := config.SetRootDirForTest(tmp)
	t.Cleanup(cleanup)
}

func TestNormalizeFilename(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"file.txt", "file.txt", false},
		{"sub/dir/file.txt", "file.txt", false},
		{"", "", true},
		{"..", "", true},
		{"a\\b.txt", "", true},
		{strings.Repeat("x", 300), "", true},
	}
	for _, c := range cases {
		got, err := NormalizeFilename(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("NormalizeFilename(%q) err=%v wantErr=%v", c.in, err, c.wantErr)
		}
		if !c.wantErr && got != c.want {
			t.Errorf("NormalizeFilename(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestWriteListDelete(t *testing.T) {
	setTestRoot(t)
	sessionID := "t1"
	dest, err := Write(sessionID, "hello.txt", strings.NewReader("hi"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dest)
	if err != nil || string(data) != "hi" {
		t.Fatalf("dest content mismatch: %q (err=%v)", data, err)
	}
	files, err := List(sessionID)
	if err != nil || len(files) != 1 || files[0].Filename != "hello.txt" {
		t.Fatalf("list mismatch: %+v err=%v", files, err)
	}
	if err := Delete(sessionID, "hello.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("expected delete; stat err=%v", err)
	}
}

func TestWriteStripsPathSegments(t *testing.T) {
	setTestRoot(t)
	dest, err := Write("t1", "../etc/passwd", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(dest) != "passwd" {
		t.Fatalf("expected dest to be basename'd, got %s", dest)
	}
	base := config.SandboxUploadsDir("t1")
	if !strings.HasPrefix(dest, base+string(filepath.Separator)) {
		t.Fatalf("dest %s escaped uploads dir %s", dest, base)
	}
}

func TestWriteRejectsSymlinkDestination(t *testing.T) {
	setTestRoot(t)
	sessionID := "t1"
	base := config.SandboxUploadsDir(sessionID)
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(base, "evil.txt")
	if err := os.Symlink("/etc/passwd", bad); err != nil {
		t.Skipf("symlink unsupported on this fs: %v", err)
	}
	_, err := Write(sessionID, "evil.txt", strings.NewReader("pwn"))
	if err == nil {
		t.Fatal("expected open to refuse symlink")
	}
	if errors.Is(err, ErrPathTraversal) {
		t.Fatalf("wrong error class: %v", err)
	}
}
