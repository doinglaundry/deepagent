package search

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestFindGlobMatchesIgnoresDir(t *testing.T) {
	tmp := t.TempDir()
	_ = os.MkdirAll(filepath.Join(tmp, "node_modules", "deep"), 0o755)
	_ = os.WriteFile(filepath.Join(tmp, "node_modules", "x.go"), []byte("//"), 0o644)
	_ = os.WriteFile(filepath.Join(tmp, "main.go"), []byte("//"), 0o644)

	matches, truncated, err := FindGlobMatches(tmp, "**/*.go", GlobOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if truncated {
		t.Fatal("unexpected truncation")
	}
	if len(matches) != 1 {
		t.Fatalf("want 1 match (node_modules ignored), got %d: %v", len(matches), matches)
	}
}

func TestFindGrepMatches(t *testing.T) {
	tmp := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmp, "a.txt"), []byte("hello world\nfoo bar\n"), 0o644)
	_ = os.WriteFile(filepath.Join(tmp, "b.txt"), []byte("nothing here\n"), 0o644)

	matches, _, err := FindGrepMatches(tmp, "world", GrepOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].LineNumber != 1 {
		t.Fatalf("want 1 match on line 1, got %v", matches)
	}
}

func TestFindGrepMatchesLineSummaryLength(t *testing.T) {
	tmp := t.TempDir()
	err := os.WriteFile(filepath.Join(tmp, "a.txt"), []byte("abcdef\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		length int
		want   string
	}{
		{name: "one", length: 1, want: "a"},
		{name: "two", length: 2, want: "ab"},
		{name: "three", length: 3, want: "..."},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			matches, _, err := FindGrepMatches(tmp, "a", GrepOpts{LineSummaryLength: test.length})
			if err != nil {
				t.Fatal(err)
			}
			if len(matches) != 1 {
				t.Fatalf("want 1 match, got %v", matches)
			}
			if matches[0].Line != test.want {
				t.Fatalf("want line %q, got %q", test.want, matches[0].Line)
			}
		})
	}
}

func TestFindGrepMatchesSkipsFIFO(t *testing.T) {
	fifoRoot := os.Getenv("DEEPAGENT_FIFO_ROOT")
	if fifoRoot != "" {
		matches, truncated, err := FindGrepMatches(fifoRoot, "needle", GrepOpts{})
		if err != nil {
			t.Fatal(err)
		}
		if truncated {
			t.Fatal("unexpected truncation")
		}
		if len(matches) != 1 || matches[0].Path != filepath.Join(fifoRoot, "regular.txt") {
			t.Fatalf("want only regular file match, got %v", matches)
		}
		return
	}
	if runtime.GOOS == "windows" {
		t.Skip("named pipes are not created with mkfifo on Windows")
	}
	mkfifoPath, err := exec.LookPath("mkfifo")
	if err != nil {
		t.Skipf("mkfifo is unavailable: %v", err)
	}

	tmp := t.TempDir()
	err = os.WriteFile(filepath.Join(tmp, "regular.txt"), []byte("needle\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	fifoPath := filepath.Join(tmp, "named-pipe")
	cmd := exec.Command(mkfifoPath, fifoPath)
	err = cmd.Run()
	if err != nil {
		t.Skipf("mkfifo is unsupported: %v", err)
	}

	cmd = exec.Command(os.Args[0], "-test.run=^TestFindGrepMatchesSkipsFIFO$", "-test.v")
	cmd.Env = append(os.Environ(), "DEEPAGENT_FIFO_ROOT="+tmp)
	err = cmd.Start()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()

	select {
	case err = <-done:
		if err != nil {
			t.Fatalf("FIFO regression subprocess failed: %v", err)
		}
	case <-timer.C:
		killErr := cmd.Process.Kill()
		waitErr := <-done
		t.Fatalf("FIFO regression subprocess hung (kill: %v, wait: %v)", killErr, waitErr)
	}
}

func TestShouldIgnoreName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"foo.go", false},
		{".git", true},
		{"node_modules", true},
		{"build.log", true},
		{"main.go.bak", true},
	}
	for _, c := range cases {
		got := ShouldIgnoreName(c.name)
		if got != c.want {
			t.Errorf("ShouldIgnoreName(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}
