package model_test

import (
	"testing"

	agentmodel "eino-cli/deepagent/model"
)

func TestResumeCannotBecomeUncorrelatedUserInput(t *testing.T) {
	for _, r := range []*agentmodel.ProtocolResume{nil, {}, {RunID: "r"}, {RunID: "r", CheckpointID: "c"}} {
		err := (agentmodel.ProtocolInput{Kind: agentmodel.InputResume, Resume: r}).Validate()
		if err == nil {
			t.Fatalf("accepted incomplete resume: %+v", r)
		}
	}
	validationErr := (agentmodel.ProtocolInput{Kind: agentmodel.InputResume, Resume: &agentmodel.ProtocolResume{RunID: "r", CheckpointID: "c", InterruptID: "i", Answer: "no"}}).Validate()
	if validationErr != nil {
		t.Fatal(validationErr)
	}
}
