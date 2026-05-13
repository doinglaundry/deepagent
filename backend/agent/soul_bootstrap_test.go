package agent

import (
	"context"
	"strings"
	"testing"

	"eino-cli/backend/soulbootstrap"
)

func TestBuildSoulBootstrapPromptIncludesTemplateAndExistingSoul(t *testing.T) {
	prompt := buildSoulBootstrapPrompt(soulbootstrap.State{
		Round:        8,
		ExistingSoul: "**Identity**\nOld",
	})
	for _, want := range []string{
		"Conversation Phases",
		"SOUL.md template",
		"**Code Style**",
		"**Never Do**",
		"ExistingSoul",
		"At round 8 or later",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestParseSoulBootstrapReplyValidatesDraftState(t *testing.T) {
	_, err := parseSoulBootstrapReply(`{"message":"hi","next_phase":"hello","ready":true}`)
	if err == nil || !strings.Contains(err.Error(), "missing draft") {
		t.Fatalf("expected missing draft error, got %v", err)
	}

	_, err = parseSoulBootstrapReply(`{"message":"hi","next_phase":"hello","draft":"x","ready":false}`)
	if err == nil || !strings.Contains(err.Error(), "draft requires ready=true") {
		t.Fatalf("expected draft/ready mismatch error, got %v", err)
	}
}

func TestBuildSoulBootstrapReplyWithModelParsesJSONFence(t *testing.T) {
	chat := &fakeChatModel{response: "```json\n{\"message\":\"next\",\"next_phase\":\"you\",\"ready\":false}\n```"}
	reply, err := buildSoulBootstrapReplyWithModel(context.Background(), chat, soulbootstrap.State{})
	if err != nil {
		t.Fatalf("buildSoulBootstrapReplyWithModel: %v", err)
	}
	if reply.Message != "next" || reply.NextPhase != soulbootstrap.PhaseYou {
		t.Fatalf("reply mismatch: %+v", reply)
	}
}
