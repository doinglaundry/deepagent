package modelhub

import (
	"context"
	"testing"
)

func TestModelConfigurationValidation(t *testing.T) {
	for _, config := range []Config{{}, {Name: "bad", Provider: "unknown", Model: "x"}, {Name: "missing", Provider: "openai"}} {
		_, err := New(context.Background(), config)
		if err == nil {
			t.Fatalf("accepted %+v", config)
		}
	}
}
func TestOpenAICompatibleModelConstructsWithoutNetwork(t *testing.T) {
	chatModel, err := New(context.Background(), Config{Name: "local", Provider: "openai", Model: "test-model", BaseURL: "http://127.0.0.1:1/v1", APIKey: "test"})
	if err != nil || chatModel == nil {
		t.Fatalf("model=%v err=%v", chatModel, err)
	}
}
