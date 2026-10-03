package protocol

import "testing"

func TestResumeCannotBecomeUncorrelatedUserInput(t *testing.T) {
	for _, r := range []*Resume{nil, {}, {RunID: "r"}, {RunID: "r", CheckpointID: "c"}} {
		err := (Input{Kind: InputResume, Resume: r}).Validate()
		if err == nil {
			t.Fatalf("accepted incomplete resume: %+v", r)
		}
	}
	validationErr := (Input{Kind: InputResume, Resume: &Resume{RunID: "r", CheckpointID: "c", InterruptID: "i", Answer: "no"}}).Validate()
	if validationErr != nil {
		t.Fatal(validationErr)
	}
}
