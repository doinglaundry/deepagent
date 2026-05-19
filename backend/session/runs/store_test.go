package runs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreSaveGetRoundTrip(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Now().UTC().Truncate(time.Second)
	want := Record{
		ID:         "run-1",
		Status:     "success",
		Prompt:     "hello",
		CreatedAt:  now,
		UpdatedAt:  now.Add(2 * time.Second),
		DurationMS: 2000,
		Output:     "world",
		Tokens:     42,
	}
	if err := store.Save(context.Background(), want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, ok, err := store.Get(context.Background(), want.ID)
	if err != nil || !ok {
		t.Fatalf("Get() ok=%v err=%v", ok, err)
	}
	if got != want {
		t.Fatalf("Get() = %#v, want %#v", got, want)
	}
}

func TestStoreListSortedByCreatedAtDesc(t *testing.T) {
	store := NewStore(t.TempDir())
	base := time.Now().UTC()
	saves := []Record{
		{ID: "a", Status: "success", CreatedAt: base.Add(-2 * time.Hour)},
		{ID: "b", Status: "error", CreatedAt: base},
		{ID: "c", Status: "success", CreatedAt: base.Add(-1 * time.Hour)},
	}
	for _, rec := range saves {
		if err := store.Save(context.Background(), rec); err != nil {
			t.Fatalf("Save(%s) error = %v", rec.ID, err)
		}
	}
	got, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	wantIDs := []string{"b", "c", "a"}
	if len(got) != len(wantIDs) {
		t.Fatalf("List() len = %d, want %d", len(got), len(wantIDs))
	}
	for i, rec := range got {
		if rec.ID != wantIDs[i] {
			t.Fatalf("List()[%d].ID = %q, want %q", i, rec.ID, wantIDs[i])
		}
	}
}

func TestStoreGetMissingReturnsFalse(t *testing.T) {
	store := NewStore(t.TempDir())
	rec, ok, err := store.Get(context.Background(), "missing")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if ok {
		t.Fatalf("Get() ok = true, want false (record = %#v)", rec)
	}
}

func TestStoreSaveSurvivesPartialWrite(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	if err := os.WriteFile(filepath.Join(dir, "run-x.json.tmp"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("seed tmp: %v", err)
	}
	rec := Record{ID: "run-x", Status: "success", CreatedAt: time.Now().UTC()}
	if err := store.Save(context.Background(), rec); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, ok, err := store.Get(context.Background(), "run-x")
	if err != nil || !ok {
		t.Fatalf("Get() ok=%v err=%v", ok, err)
	}
	if got.Status != "success" {
		t.Fatalf("Status = %q, want success", got.Status)
	}
}
