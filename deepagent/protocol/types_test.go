package protocol

import "testing"

func TestResumeCannotBecomeUncorrelatedUserInput(t *testing.T) {
	for _, r := range []*Resume{nil, {}, {RunID: "r"}, {RunID: "r", CheckpointID: "c"}} {
		if err := (Input{Kind: InputResume, Resume: r}).Validate(); err == nil {
			t.Fatalf("accepted incomplete resume: %+v", r)
		}
	}
	if err := (Input{Kind: InputResume, Resume: &Resume{RunID: "r", CheckpointID: "c", InterruptID: "i", Answer: "no"}}).Validate(); err != nil {
		t.Fatal(err)
	}
}
