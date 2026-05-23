package autodream

import (
	"fmt"
	"strings"
)

func BuildAutoDreamExtra(memoryRoot string, sessionIDs []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Dream memory root: %s\n", memoryRoot)
	fmt.Fprintf(&b, "Memory index: %s/MEMORY.md\n", strings.TrimRight(memoryRoot, "/"))
	if len(sessionIDs) > 0 {
		b.WriteString("Sessions to consolidate:\n")
		for _, sessionID := range sessionIDs {
			fmt.Fprintf(&b, "- %s\n", sessionID)
		}
	}
	return b.String()
}

func BuildConsolidationPrompt(memoryRoot, transcriptDir string, sessionIDs []string) string {
	var b strings.Builder
	b.WriteString("You are running an automatic memory consolidation pass.\n")
	b.WriteString("Read the listed JSONL transcripts and update markdown memory only inside the dream memory root.\n")
	b.WriteString("Keep MEMORY.md as a concise index and create topic files when a detail is too long for the index.\n")
	b.WriteString("Do not modify repository source files or any path outside the dream memory root.\n\n")
	fmt.Fprintf(&b, "Dream memory root: %s\n", memoryRoot)
	fmt.Fprintf(&b, "Transcript dir: %s\n", transcriptDir)
	b.WriteString("Transcript files:\n")
	for _, sessionID := range sessionIDs {
		fmt.Fprintf(&b, "- %s.jsonl\n", sessionID)
	}
	return b.String()
}
