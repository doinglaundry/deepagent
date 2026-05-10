package agent

import (
	"fmt"
	"sort"
	"strings"

	memorystore "eino-cli/backend/memory/store"
)

// charsPerToken approximates token count as len(text)/4. Cheap, deterministic,
// good enough for budgeting; replace with a real tokenizer here if drift hurts.
const charsPerToken = 4

// formatMemoryForInjection mirrors deer-flow format_memory_for_injection:
// emits "User Context:" / "History:" / "Facts:" sections in that order,
// drops empty sections, and budgets fact lines under maxTokens. maxTokens<=0
// means "no budget".
func formatMemoryForInjection(data memorystore.MemoryData, maxTokens int) string {
	var sections []string

	if userText := renderUserSection(data.User); userText != "" {
		sections = append(sections, userText)
	}
	if histText := renderHistorySection(data.History); histText != "" {
		sections = append(sections, histText)
	}

	if len(data.Facts) > 0 {
		baseTokens := countTokens(strings.Join(sections, "\n\n"))
		var sepTokens int
		if len(sections) > 0 {
			sepTokens = countTokens("\n\nFacts:\n")
		} else {
			sepTokens = countTokens("Facts:\n")
		}
		factsBlock, _ := renderFactsSection(data.Facts, baseTokens+sepTokens, maxTokens)
		if factsBlock != "" {
			sections = append(sections, factsBlock)
		}
	}

	if len(sections) == 0 {
		return ""
	}

	result := strings.Join(sections, "\n\n")
	if maxTokens > 0 {
		total := countTokens(result)
		if total > maxTokens {
			// chars/4 tokenizer makes char_per_token == 4 deterministically;
			// keep the multiplication form so swapping in a real tokenizer
			// later doesn't require rewriting the truncation rule.
			charPerToken := len(result) / max(total, 1)
			target := int(float64(maxTokens) * float64(charPerToken) * 0.95)
			if target > 0 && target < len(result) {
				result = result[:target] + "\n..."
			}
		}
	}
	return result
}

func countTokens(s string) int { return len(s) / charsPerToken }

func renderUserSection(u memorystore.UserContext) string {
	lines := make([]string, 0, 3)
	if s := strings.TrimSpace(u.WorkContext.Summary); s != "" {
		lines = append(lines, "- Work: "+s)
	}
	if s := strings.TrimSpace(u.PersonalContext.Summary); s != "" {
		lines = append(lines, "- Personal: "+s)
	}
	if s := strings.TrimSpace(u.TopOfMind.Summary); s != "" {
		lines = append(lines, "- Current Focus: "+s)
	}
	if len(lines) == 0 {
		return ""
	}
	return "User Context:\n" + strings.Join(lines, "\n")
}

func renderHistorySection(h memorystore.History) string {
	lines := make([]string, 0, 3)
	if s := strings.TrimSpace(h.RecentMonths.Summary); s != "" {
		lines = append(lines, "- Recent: "+s)
	}
	if s := strings.TrimSpace(h.EarlierContext.Summary); s != "" {
		lines = append(lines, "- Earlier: "+s)
	}
	if s := strings.TrimSpace(h.LongTermBackground.Summary); s != "" {
		lines = append(lines, "- Background: "+s)
	}
	if len(lines) == 0 {
		return ""
	}
	return "History:\n" + strings.Join(lines, "\n")
}

// renderFactsSection sorts facts by confidence desc and emits as many lines
// as fit under maxTokens (counting against runningTokens so the caller can
// account for what it has already emitted). Returns the rendered block plus
// the per-section token tally so callers can keep budgeting downstream.
func renderFactsSection(facts []memorystore.Fact, runningTokens, maxTokens int) (string, int) {
	if len(facts) == 0 {
		return "", runningTokens
	}

	sorted := make([]memorystore.Fact, len(facts))
	copy(sorted, facts)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Confidence > sorted[j].Confidence })

	lines := make([]string, 0, len(sorted))
	for _, f := range sorted {
		content := strings.TrimSpace(f.Content)
		if content == "" {
			continue
		}
		category := f.Category
		if category == "" {
			category = "context"
		}
		conf := memorystore.CoerceConfidence(f.Confidence)
		var line string
		if category == "correction" && strings.TrimSpace(f.SourceError) != "" {
			line = fmt.Sprintf("- [%s | %.2f] %s (avoid: %s)", category, conf, content, strings.TrimSpace(f.SourceError))
		} else {
			line = fmt.Sprintf("- [%s | %.2f] %s", category, conf, content)
		}

		// First line glues directly under "Facts:\n"; subsequent lines need
		// their own newline. Keep the prefix on the line we're measuring so
		// the budget reflects what the renderer will actually emit.
		var measured string
		if len(lines) == 0 {
			measured = line
		} else {
			measured = "\n" + line
		}
		lineTokens := countTokens(measured)
		if maxTokens > 0 && runningTokens+lineTokens > maxTokens {
			break
		}
		lines = append(lines, line)
		runningTokens += lineTokens
	}

	if len(lines) == 0 {
		return "", runningTokens
	}
	return "Facts:\n" + strings.Join(lines, "\n"), runningTokens
}
