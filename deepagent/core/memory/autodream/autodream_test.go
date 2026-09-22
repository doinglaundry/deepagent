package autodream

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestListAndFilterJSONLSessions(t *testing.T) {
	dir := t.TempDir()
	oldTime := time.Now().Add(-2 * time.Hour)
	newTime := time.Now()
	for name, modTime := range map[string]time.Time{
		"old.jsonl":     oldTime,
		"new.jsonl":     newTime,
		"current.jsonl": newTime,
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, modTime, modTime); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "skip.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	candidates, err := ListJSONLSessionCandidates(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 3 {
		t.Fatalf("candidates len = %d, want 3", len(candidates))
	}
	sessionIDs := FilterSessionsTouchedSince(candidates, oldTime.Add(time.Minute), "current")
	if strings.Join(sessionIDs, ",") != "new" {
		t.Fatalf("filtered session ids = %#v, want [new]", sessionIDs)
	}
}

func TestConsolidationLockRollbackMissing(t *testing.T) {
	memoryRoot := t.TempDir()
	lock, err := TryAcquireConsolidationLock(memoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	if lock == nil {
		t.Fatal("expected acquired lock")
	}
	if !lock.previousWasMissing || !lock.previousModTime.IsZero() {
		t.Fatalf("unexpected lock snapshot: %#v", lock)
	}
	if lastConsolidatedAt, err := ReadLastConsolidatedAt(memoryRoot); err != nil || lastConsolidatedAt.IsZero() {
		t.Fatalf("lock should exist after acquire, last=%v err=%v", lastConsolidatedAt, err)
	}

	RollbackConsolidationLock(lock)
	if _, err := os.Stat(filepath.Join(memoryRoot, ".consolidate-lock")); !os.IsNotExist(err) {
		t.Fatalf("rollback should remove new lock, err=%v", err)
	}
}

func TestBuildConsolidationPrompt(t *testing.T) {
	prompt := BuildConsolidationPrompt("/mem", "/transcripts", []string{"s1", "s2"})
	for _, want := range []string{"/mem", "/transcripts", "s1.jsonl", "s2.jsonl", "only inside the dream memory root"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}
