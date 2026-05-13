package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"eino-cli/backend/config"
	"eino-cli/backend/soulbootstrap"
)

const soulBootstrapTimeout = 60 * time.Second

func BuildSoulBootstrapReply(ctx context.Context, cfg *config.Config, state soulbootstrap.State) (soulbootstrap.Reply, error) {
	if cfg == nil {
		return soulbootstrap.Reply{}, fmt.Errorf("build soul bootstrap model: nil config")
	}
	modelConfig := cfg.Models[cfg.DefaultModel]
	if modelConfig == nil {
		return soulbootstrap.Reply{}, fmt.Errorf("build soul bootstrap model: default model not found")
	}
	chatModel, err := buildChatModel(ctx, modelConfig)
	if err != nil {
		return soulbootstrap.Reply{}, fmt.Errorf("build soul bootstrap model: %w", err)
	}
	return buildSoulBootstrapReplyWithModel(ctx, chatModel, state)
}

func buildSoulBootstrapReplyWithModel(
	ctx context.Context,
	chatModel model.BaseChatModel,
	state soulbootstrap.State,
) (soulbootstrap.Reply, error) {
	if chatModel == nil {
		return soulbootstrap.Reply{}, fmt.Errorf("soul bootstrap: nil model")
	}

	runCtx, cancel := context.WithTimeout(ctx, soulBootstrapTimeout)
	defer cancel()

	resp, err := chatModel.Generate(runCtx, []*schema.Message{
		schema.UserMessage(buildSoulBootstrapPrompt(state)),
	})
	if err != nil {
		return soulbootstrap.Reply{}, fmt.Errorf("soul bootstrap llm: %w", err)
	}
	if resp == nil || strings.TrimSpace(resp.Content) == "" {
		return soulbootstrap.Reply{}, fmt.Errorf("soul bootstrap llm: empty response")
	}
	return parseSoulBootstrapReply(resp.Content)
}

func parseSoulBootstrapReply(content string) (soulbootstrap.Reply, error) {
	var reply soulbootstrap.Reply
	payload := strings.TrimSpace(stripMarkdownFence(content))
	if err := json.Unmarshal([]byte(payload), &reply); err != nil {
		return soulbootstrap.Reply{}, fmt.Errorf("parse soul bootstrap reply: %w", err)
	}
	if strings.TrimSpace(reply.Message) == "" {
		return soulbootstrap.Reply{}, fmt.Errorf("parse soul bootstrap reply: empty message")
	}
	if reply.NextPhase == "" {
		reply.NextPhase = soulbootstrap.PhaseHello
	}
	if !reply.Ready && strings.TrimSpace(reply.Draft) != "" {
		return soulbootstrap.Reply{}, fmt.Errorf("parse soul bootstrap reply: draft requires ready=true")
	}
	if reply.Ready && strings.TrimSpace(reply.Draft) == "" {
		return soulbootstrap.Reply{}, fmt.Errorf("parse soul bootstrap reply: ready response missing draft")
	}
	reply.Draft = strings.TrimSpace(reply.Draft)
	return reply, nil
}

func buildSoulBootstrapPrompt(state soulbootstrap.State) string {
	current, _ := json.MarshalIndent(state, "", "  ")
	return fmt.Sprintf(`You are running a DeerFlow-style SOUL.md bootstrap conversation for a CLI agent.

Ground Rules:
- One phase at a time. Ask 1-3 questions max per round.
- Converse, don't interrogate. Mirror the user's vocabulary and energy.
- Adapt pacing. Short answers: probe once then advance. Long answers: distill and advance.
- Never expose the template. The user is having a conversation, not filling a form.
- At round 8 or later, generate a draft with available information.

Conversation Phases:
1. Hello: establish preferred language.
2. You: learn who the user is, what drains them, agent name, relationship framing.
3. Personality: behavior, communication style, pushback preference, autonomy, boundaries.
4. Depth: failure philosophy, long-term vision, blind spots, dealbreakers.

SOUL.md template:
**Identity**
[AI Name] - [User Name]'s [relationship framing], not [contrast]. Goal: [long-term aspiration]. Handle [specific domains from pain points] so [User Name] focuses on [what matters to them].

**Core Traits**
[Trait 1 - behavioral rule derived from conversation].
[Trait 2 - behavioral rule].
[Trait 3 - behavioral rule].
[Trait 4 - failure handling rule: allowed to fail, forbidden to repeat].

**Communication**
[Tone description matching the user]. Default language: [language]. [Language-switching rules if any].

**Code Style**
[Concrete coding preferences: structure, naming, tests, comments, risk tolerance.]

**Growth**
Learn [User Name] through every conversation - thinking patterns, preferences, blind spots, aspirations. Over time, anticipate needs and act on [User Name]'s behalf with increasing accuracy. Early stage: proactively ask casual/personal questions after tasks to deepen understanding of who [User Name] is. Full of curiosity, willing to explore.

**Never Do**
[Hard prohibitions, destructive-operation rules, files or workflows to avoid.]

**Lessons Learned**
_(Mistakes and insights recorded here to avoid repeating them.)_

Generation rules:
- Final SOUL.md should be English, concise, and about 300 words.
- Every sentence must trace back to the user or a clear implication.
- Core Traits must be behavioral rules, not adjectives.
- Code Style and Never Do are required for this repository.
- Do not include API keys, tokens, or secrets.
- If existing_soul is present, update it instead of overwriting unspecified old preferences.

Current bootstrap state:
%s

Return ONLY valid JSON with this shape:
{
  "message": "next user-facing message or draft intro",
  "next_phase": "hello|you|personality|depth|draft",
  "fields": { "preferred_language": "", "user_name": "", "user_role": "", "pain_points": "", "agent_name": "", "relationship": "", "core_traits": "", "communication_style": "", "pushback_preference": "", "autonomy_level": "", "failure_philosophy": "", "long_term_vision": "", "blind_spots": "", "boundaries": "", "code_style": "", "never_do": "" },
  "draft": "",
  "ready": false
}
When ready=true, draft must contain the complete SOUL.md markdown.`, string(current))
}

func stripMarkdownFence(content string) string {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "```") {
		return content
	}
	lines := strings.Split(content, "\n")
	if len(lines) < 2 {
		return content
	}
	if strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
		return strings.Join(lines[1:len(lines)-1], "\n")
	}
	return content
}
