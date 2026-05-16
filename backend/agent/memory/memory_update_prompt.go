package memory

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"

	memorystore "eino-cli/backend/memory/store"
)

// messageContentMaxLen caps a single rendered turn so a giant pasted code
// block can't dominate the update prompt and dilute the actual signal.
const messageContentMaxLen = 1000

// memoryUpdatePromptTemplate is a 1:1 port of deer-flow MEMORY_UPDATE_PROMPT.
// Placeholders use double-underscore form (__X__) instead of Python's `{x}`
// so the JSON example blocks below need no `{{` escaping; buildUpdatePrompt
// substitutes via plain strings.Replace.
const memoryUpdatePromptTemplate = `You are a memory management system. Your task is to analyze a conversation and update the user's memory profile.

Current Memory State:
<current_memory>
__CURRENT_MEMORY__
</current_memory>

New Conversation to Process:
<conversation>
__CONVERSATION__
</conversation>

Instructions:
1. Analyze the conversation for important information about the user
2. Extract relevant facts, preferences, and context with specific details (numbers, names, technologies)
3. Update the memory sections as needed following the detailed length guidelines below

Before extracting facts, perform a structured reflection on the conversation:
1. Error/Retry Detection: Did the agent encounter errors, require retries, or produce incorrect results?
   If yes, record the root cause and correct approach as a high-confidence fact with category "correction".
2. User Correction Detection: Did the user correct the agent's direction, understanding, or output?
   If yes, record the correct interpretation or approach as a high-confidence fact with category "correction".
   Include what went wrong in "sourceError" only when category is "correction" and the mistake is explicit in the conversation.
3. Project Constraint Discovery: Were any project-specific constraints discovered during the conversation?
   If yes, record them as facts with the most appropriate category and confidence.

__CORRECTION_HINT__

Memory Section Guidelines:

**User Context** (Current state - concise summaries):
- workContext: Professional role, company, key projects, main technologies (2-3 sentences)
- personalContext: Languages, communication preferences, key interests (1-2 sentences)
- topOfMind: Multiple ongoing focus areas and priorities (3-5 sentences, detailed paragraph)
  Note: This captures SEVERAL concurrent focus areas, not just one task

**History** (Temporal context - rich paragraphs):
- recentMonths: Detailed summary of recent activities (4-6 sentences or 1-2 paragraphs)
  Timeline: Last 1-3 months of interactions
- earlierContext: Important historical patterns (3-5 sentences or 1 paragraph)
  Timeline: 3-12 months ago
- longTermBackground: Persistent background and foundational context (2-4 sentences)
  Timeline: Overall/foundational information

**Facts Extraction**:
- Extract specific, quantifiable details (e.g., "16k+ GitHub stars", "200+ datasets")
- Include proper nouns (company names, project names, technology names)
- Preserve technical terminology and version numbers
- Categories:
  * preference: Tools, styles, approaches user prefers/dislikes
  * knowledge: Specific expertise, technologies mastered, domain knowledge
  * context: Background facts (job title, projects, locations, languages)
  * behavior: Working patterns, communication habits, problem-solving approaches
  * goal: Stated objectives, learning targets, project ambitions
  * correction: Explicit agent mistakes or user corrections, including the correct approach
- Confidence levels:
  * 0.9-1.0: Explicitly stated facts ("I work on X", "My role is Y")
  * 0.7-0.8: Strongly implied from actions/discussions
  * 0.5-0.6: Inferred patterns (use sparingly, only for clear patterns)

Output Format (JSON):
{
  "user": {
    "workContext": { "summary": "...", "shouldUpdate": true },
    "personalContext": { "summary": "...", "shouldUpdate": true },
    "topOfMind": { "summary": "...", "shouldUpdate": true }
  },
  "history": {
    "recentMonths": { "summary": "...", "shouldUpdate": true },
    "earlierContext": { "summary": "...", "shouldUpdate": true },
    "longTermBackground": { "summary": "...", "shouldUpdate": true }
  },
  "newFacts": [
    { "content": "...", "category": "preference|knowledge|context|behavior|goal|correction", "confidence": 0.0, "kind": "enduring|episodic", "expiresAt": "2026-05-15T18:00:00Z" }
  ],
  "factsToRemove": ["fact_id_1", "fact_id_2"]
}

Important Rules:
- Only set shouldUpdate=true if there's meaningful new information
- Follow length guidelines: workContext/personalContext are concise (1-3 sentences), topOfMind and history sections are detailed (paragraphs)
- Include specific metrics, version numbers, and proper nouns in facts
- Only add facts that are clearly stated (0.9+) or strongly implied (0.7+)
- Use category "correction" for explicit agent mistakes or user corrections; assign confidence >= 0.95 when the correction is explicit
- Include "sourceError" only for explicit correction facts when the prior mistake or wrong approach is clearly stated; omit it otherwise
- Remove facts that are contradicted by new information
- When updating topOfMind, integrate new focus areas while removing completed/abandoned ones
- For history sections, integrate new information chronologically into appropriate time period
- Preserve technical accuracy - keep exact names of technologies, companies, projects
- Focus on information useful for future interactions and personalization
- Dedup: before adding a newFact, scan <current_memory>.facts. If the same
  fact is already present (literal or semantic match), DO NOT add it again
  — the write side merges confidence automatically. If the wording is
  different but semantically equivalent, add the new one AND put the old
  fact_id into factsToRemove.
- Kind classification (required for every newFact):
  * enduring: long-term preferences, work background, sustained goals,
    knowledge — the default when uncertain.
  * episodic: one-shot conversational goals, transient debugging context,
    single-question artefacts (e.g. "user asked for line count of
    CHANGELOG.md"). These auto-expire on the write side; you may also set
    an explicit expiresAt (ISO-8601 UTC) but it is optional.

Return ONLY valid JSON, no explanation or markdown.`

// buildUpdatePrompt renders the update prompt by substituting the three
// __X__ placeholders. The current memory is JSON-encoded with two-space
// indent so the LLM sees the exact on-disk shape.
func buildUpdatePrompt(current memorystore.MemoryData, conversation string) (string, error) {
	payload, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal current memory: %w", err)
	}

	correction, reinforcement := detectSignals(nil)
	hint := buildCorrectionHint(correction, reinforcement)

	out := memoryUpdatePromptTemplate
	out = strings.Replace(out, "__CURRENT_MEMORY__", string(payload), 1)
	out = strings.Replace(out, "__CONVERSATION__", conversation, 1)
	out = strings.Replace(out, "__CORRECTION_HINT__", hint, 1)
	return out, nil
}

// formatConversationForUpdate emits the "User: ..." / "Assistant: ..." block
// that goes into the prompt. System / tool messages are skipped (deer-flow
// parity). Long messages are truncated so a 50KB code paste doesn't crowd
// out the actual signal in the LLM's context.
func formatConversationForUpdate(messages []*schema.Message) string {
	lines := make([]string, 0, len(messages))
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}
		if len(content) > messageContentMaxLen {
			content = content[:messageContentMaxLen] + "..."
		}
		switch msg.Role {
		case schema.User:
			lines = append(lines, "User: "+content)
		case schema.Assistant:
			lines = append(lines, "Assistant: "+content)
		}
	}
	return strings.Join(lines, "\n\n")
}

// detectSignals is the seam where future heuristics for "user just corrected
// us" / "user just praised us" go. Returns false, false today so the prompt
// never receives a hint; deer-flow's keyword grep is intentionally not ported
// (AGENTS.md "don't write features the user didn't ask for").
func detectSignals(_ []*schema.Message) (correction, reinforcement bool) {
	return false, false
}

// buildCorrectionHint formats the optional hint paragraph for the prompt.
// Empty when no signals fire, which is the only case today.
func buildCorrectionHint(correction, reinforcement bool) string {
	if !correction && !reinforcement {
		return ""
	}
	var parts []string
	if correction {
		parts = append(parts, "IMPORTANT: Explicit correction signals were detected in this conversation. "+
			"Pay special attention to what the agent got wrong, what the user corrected, "+
			"and record the correct approach as a fact with category "+
			`"correction" and confidence >= 0.95 when appropriate.`)
	}
	if reinforcement {
		parts = append(parts, "IMPORTANT: Positive reinforcement signals were detected in this conversation. "+
			"The user explicitly confirmed the agent's approach was correct or helpful. "+
			"Record the confirmed approach, style, or preference as a fact with category "+
			`"preference" or "behavior" and confidence >= 0.9 when appropriate.`)
	}
	return strings.Join(parts, "\n")
}
