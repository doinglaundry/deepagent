package store

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestStore_LoadMissingReturnsEmpty(t *testing.T) {
	s := NewStore(t.TempDir())
	data, err := s.Load("")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if data.Version != memoryFormatVersion {
		t.Fatalf("missing file should yield empty (versioned), got %+v", data)
	}
	if data.LastUpdated == "" {
		t.Fatalf("empty data should still stamp LastUpdated")
	}
	if len(data.Facts) != 0 {
		t.Fatalf("missing file should have no facts, got %d", len(data.Facts))
	}
}

func TestStore_LoadCorruptReturnsEmptyNoError(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	err := os.WriteFile(filepath.Join(dir, "global.json"), []byte("{not json"), 0o644)
	if err != nil {
		t.Fatalf("seed bad file: %v", err)
	}

	data, err := s.Load("")
	if err != nil {
		t.Fatalf("corrupt file should not surface error, got %v", err)
	}
	if data.Version != memoryFormatVersion {
		t.Fatalf("corrupt file should yield empty payload, got %+v", data)
	}
}

func TestStore_SaveLoadRoundTrip(t *testing.T) {
	s := NewStore(t.TempDir())

	in := GetEmptyMemoryData()
	in.User.WorkContext = Section{Summary: "Go backend dev", UpdatedAt: "2026-05-10T08:00:00Z"}
	in.Facts = []Fact{{
		ID:         NewFactID(),
		Content:    "prefers tabs",
		Category:   "preference",
		Confidence: 0.9,
		CreatedAt:  "2026-05-10T08:00:00Z",
		Source:     "llm",
	}}

	err := s.Save("alice", in)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	out, err := s.Load("alice")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.User.WorkContext.Summary != "Go backend dev" {
		t.Fatalf("WorkContext lost in round-trip: %+v", out.User.WorkContext)
	}
	if len(out.Facts) != 1 || out.Facts[0].Content != "prefers tabs" {
		t.Fatalf("Facts lost in round-trip: %+v", out.Facts)
	}
}

func TestStore_GlobalAndAgentSeparated(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	err := s.Save("", GetEmptyMemoryData())
	if err != nil {
		t.Fatalf("save global: %v", err)
	}
	err = s.Save("alice", GetEmptyMemoryData())
	if err != nil {
		t.Fatalf("save agent: %v", err)
	}

	if _, err = os.Stat(filepath.Join(dir, "global.json")); err != nil {
		t.Errorf("global.json not at expected path: %v", err)
	}
	if _, err = os.Stat(filepath.Join(dir, "agents", "alice.json")); err != nil {
		t.Errorf("agents/alice.json not at expected path: %v", err)
	}
}

func TestStore_RejectsInvalidAgentName(t *testing.T) {
	s := NewStore(t.TempDir())
	bad := []string{"../etc/passwd", "foo/bar", "_leading", "with space", "a" + string(make([]byte, 100))}
	for _, name := range bad {
		err := s.Save(name, GetEmptyMemoryData())
		if err == nil {
			t.Errorf("Save(%q) should reject invalid name", name)
		}
		_, err = s.Load(name)
		if err == nil {
			t.Errorf("Load(%q) should reject invalid name", name)
		}
	}
}

func TestStore_SaveLeavesNoTmpResidue(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	err := s.Save("alice", GetEmptyMemoryData())
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Walk the agents dir and assert nothing matches *.tmp.
	entries, err := os.ReadDir(filepath.Join(dir, "agents"))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("residual tmp file: %s", e.Name())
		}
	}
}

func TestCoerceConfidence(t *testing.T) {
	cases := []struct {
		in, want float64
	}{
		{0, 0},
		{0.5, 0.5},
		{1, 1},
		{-0.1, 0},
		{2.0, 1},
		{math.NaN(), 0},
		{math.Inf(1), 0},
		{math.Inf(-1), 0},
	}
	for _, c := range cases {
		got := CoerceConfidence(c.in)
		if got != c.want {
			t.Errorf("CoerceConfidence(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestStore_LoadCoercesFactConfidence(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	in := GetEmptyMemoryData()
	in.Facts = []Fact{
		{ID: "fact_one", Content: "a", Confidence: 1.5},
		{ID: "fact_two", Content: "b", Confidence: -0.5},
	}
	err := s.Save("alice", in)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	out, err := s.Load("alice")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.Facts[0].Confidence != 1 || out.Facts[1].Confidence != 0 {
		t.Errorf("Load did not coerce confidences: %+v", out.Facts)
	}
}

// TestFact_IsExpired locks down the four cases the renderer / sweeper rely
// on. ISO-8601 strings sort lexicographically in time order so plain string
// comparison must keep matching wall-clock semantics.
func TestFact_IsExpired(t *testing.T) {
	const now = "2026-05-15T17:00:00Z"
	cases := []struct {
		name string
		fact Fact
		want bool
	}{
		{"legacy missing kind", Fact{Content: "x"}, false},
		{"enduring with future expiry", Fact{Kind: FactKindEnduring, ExpiresAt: "2099-01-01T00:00:00Z"}, false},
		{"episodic without expiry", Fact{Kind: FactKindEpisodic}, false},
		{"episodic future", Fact{Kind: FactKindEpisodic, ExpiresAt: "2099-01-01T00:00:00Z"}, false},
		{"episodic past", Fact{Kind: FactKindEpisodic, ExpiresAt: "2020-01-01T00:00:00Z"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.fact.IsExpired(now)
			if got != c.want {
				t.Errorf("IsExpired = %v, want %v (fact=%+v)", got, c.want, c.fact)
			}
		})
	}
}
