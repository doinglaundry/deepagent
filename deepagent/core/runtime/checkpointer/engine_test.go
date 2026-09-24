package checkpointer

import (
	"context"
	"eino-cli/deepagent/protocol"
	"encoding/json"
	"errors"
	"os"
	"testing"
)

func TestEngineMigrationRejectsAmbiguousStateWithoutOverwriting(t *testing.T) {
	fixture, err := os.ReadFile("testdata/legacy_engine_approval.json")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*engineState){
		"pending wrong thread": func(s *engineState) {
			s.Pending = []protocol.Input{{ID: "pending", ThreadID: "other", Kind: protocol.InputUser, Text: "hello"}}
		},
		"pending control": func(s *engineState) { s.Pending = []protocol.Input{{ID: "pending", Kind: protocol.InputClose}} },
		"pending unknown media": func(s *engineState) {
			s.Pending = []protocol.Input{{ID: "pending", Kind: protocol.InputUser, Parts: []protocol.Part{{Type: "unknown", URL: "test"}}}}
		},
		"pending conflicting redelivery": func(s *engineState) {
			s.Pending = []protocol.Input{{ID: "pending", Kind: protocol.InputUser, Text: "one"}, {ID: "pending", Kind: protocol.InputUser, Text: "different"}}
		},

		"wrong thread":             func(s *engineState) { s.ThreadID = "other" },
		"wrong run":                func(s *engineState) { s.RunID = "other" },
		"wrong checkpoint":         func(s *engineState) { s.CheckpointID = "other" },
		"missing block":            func(s *engineState) { s.Block = nil },
		"unknown outcome":          func(s *engineState) { s.ToolIndex = 0 },
		"lost completed result":    func(s *engineState) { s.Messages = s.Messages[:2] },
		"wrong result":             func(s *engineState) { s.Messages[2].ToolCallID = "other" },
		"duplicate call":           func(s *engineState) { s.Calls[1].ID = s.Calls[0].ID },
		"changed arguments":        func(s *engineState) { s.Block.Arguments = `{"different":true}` },
		"pending input without ID": func(s *engineState) { s.Pending = []protocol.Input{{Kind: protocol.InputUser, Text: "pending"}} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			var old engineState
			if err := json.Unmarshal(fixture, &old); err != nil {
				t.Fatal(err)
			}
			mutate(&old)
			raw, err := json.Marshal(old)
			if err != nil {
				t.Fatal(err)
			}
			inner := &memoryStore{data: map[string][]byte{"fixture": raw}}
			_, exists, err := New(inner, "thread", "run", "core-graph-v1").Get(context.Background(), "fixture")
			if err == nil || exists {
				t.Fatalf("ambiguous state accepted: exists=%v err=%v", exists, err)
			}
			if string(inner.data["fixture"]) != string(raw) {
				t.Fatal("failed migration overwrote source")
			}
		})
	}
}

type failedEngineMigrationStore struct {
	*memoryStore
	failure error
}

func (s failedEngineMigrationStore) Set(context.Context, string, []byte) error { return s.failure }
func TestEngineMigrationSaveFailureDoesNotExposeUncommittedSnapshot(t *testing.T) {
	raw, err := os.ReadFile("testdata/legacy_engine_approval.json")
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("migration save failed")
	inner := failedEngineMigrationStore{&memoryStore{data: map[string][]byte{"fixture": raw}}, failure}
	snapshot, exists, err := New(inner, "thread", "run", "core-graph-v1").Get(context.Background(), "fixture")
	if !errors.Is(err, failure) || exists || snapshot != nil {
		t.Fatalf("snapshot exposed before commit: %v %v", exists, err)
	}
	if string(inner.data["fixture"]) != string(raw) {
		t.Fatal("save failure overwrote source")
	}
}
