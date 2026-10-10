package localmodel

import (
	"bytes"
	agentmodel "eino-cli/deepagent/model"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestDatasetSplitsExamplesWithoutOverlap(t *testing.T) {
	trainingMessages := make([][]*agentmodel.Message, 30)
	for i := range trainingMessages {
		trainingMessages[i] = []*agentmodel.Message{agentmodel.NewUserMessage(fmt.Sprint("question ", i)), agentmodel.NewAssistantMessage("answer", nil)}
	}
	trainingDirectory := t.TempDir()
	err := writeTrainingDatasets(trainingDirectory, trainingMessages)
	if err != nil {
		t.Fatal(err)
	}
	partitionByQuestion := map[string]string{}
	for datasetName, expectedExampleCount := range map[string]int{"train": 24, "valid": 2, "test": 4} {
		file, err := os.Open(filepath.Join(trainingDirectory, datasetName+".jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		decoder := json.NewDecoder(file)
		exampleCount := 0
		for decoder.More() {
			var trainingConversation struct {
				Messages []*agentmodel.Message `json:"messages"`
			}
			err = decoder.Decode(&trainingConversation)
			if err != nil {
				t.Fatal(err)
			}
			question := trainingConversation.Messages[0].Content
			if partitionByQuestion[question] != "" {
				t.Fatal("example leaked between partitions")
			}
			partitionByQuestion[question] = datasetName
			exampleCount++
		}
		file.Close()
		if exampleCount != expectedExampleCount {
			t.Fatalf("%s=%d", datasetName, exampleCount)
		}
	}
	trainingMessages = append(trainingMessages, []*agentmodel.Message{
		agentmodel.NewSystemMessage("Answer concisely."), agentmodel.NewUserMessage("question 0"), agentmodel.NewAssistantMessage("answer", nil), agentmodel.NewUserMessage("follow-up"), agentmodel.NewAssistantMessage("confirmed correction", nil),
	})
	err = writeTrainingDatasets(trainingDirectory, trainingMessages)
	if err != nil {
		t.Fatal(err)
	}
	for _, datasetName := range []string{"train", "valid", "test"} {
		datasetJSONL, err := os.ReadFile(filepath.Join(trainingDirectory, datasetName+".jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		containsCorrection := bytes.Contains(datasetJSONL, []byte("confirmed correction"))
		if containsCorrection && partitionByQuestion["question 0"] != datasetName {
			t.Fatal("longer conversation moved to another partition")
		}
	}
	err = writeTrainingDatasets(t.TempDir(), trainingMessages[:9])
	if err == nil {
		t.Fatal("trained without held-out examples")
	}
}
