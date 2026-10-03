package modelhub

import (
	"context"
	"testing"
)

func TestModelConfigurationValidation(t *testing.T) {
	for _, c := range []Config{{}, {Name: "bad", Provider: "unknown", Model: "x"}, {Name: "missing", Provider: "openai"}} {
		_, err := New(context.Background(), c)
		if err == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
}
func TestOpenAICompatibleModelConstructsWithoutNetwork(t *testing.T) {
	m, err := New(context.Background(), Config{Name: "local", Provider: "openai", Model: "test-model", BaseURL: "http://127.0.0.1:1/v1", APIKey: "test"})
	if err != nil || m == nil {
		t.Fatalf("model=%v err=%v", m, err)
	}
}
