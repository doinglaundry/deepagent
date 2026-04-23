package repl

import (
	"fmt"
	"strings"

	"eino-cli/internal/cli/render"
	memoryretrieval "eino-cli/internal/memory/retrieval"
	"eino-cli/internal/session"
	"eino-cli/internal/session/checkpoint"
	"eino-cli/internal/session/inject"
)

func resumeMessage(sess session.Session, snapshot checkpoint.Snapshot, retriever *memoryretrieval.Retriever) (render.Message, error) {
	context, err := inject.Build(sess, snapshot, retriever)
	if err != nil {
		return render.Message{}, err
	}
	if !context.ResumeRequired {
		return render.Message{Kind: "resume", Content: "resume: none"}, nil
	}
	lines := []string{
		fmt.Sprintf("resume session: %s", context.SessionID),
		fmt.Sprintf("workspace: %s", context.WorkspaceRoot),
	}
	if strings.TrimSpace(context.LastInput) != "" {
		lines = append(lines, fmt.Sprintf("last input: %s", context.LastInput))
	}
	if len(context.Memory) > 0 {
		lines = append(lines, "memory context:")
		for _, item := range context.Memory {
			lines = append(lines, fmt.Sprintf("- %s", item))
		}
	}
	return render.Message{Kind: "resume", Content: strings.Join(lines, "\n")}, nil
}
