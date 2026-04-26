package repl

import (
	"fmt"
	"strings"

	"eino-cli/internal/cli/render"
	memorystore "eino-cli/internal/memory/store"
	"eino-cli/internal/session"
)

func resumeMessage(sess session.Session, turn session.Turn, store *memorystore.Store) (render.Message, error) {
	context, err := BuildContext(sess, turn, store)
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

func BuildContext(sess session.Session, turn session.Turn, store *memorystore.Store) (ResumeContext, error) {
	memories, err := store.Find("")
	if err != nil {
		return ResumeContext{}, err
	}
	var userMemories []string
	for _, m := range memories {
		if !strings.HasPrefix(m.Content, "task ") {
			userMemories = append(userMemories, m.Content)
		}
	}
	if len(userMemories) > 5 {
		userMemories = userMemories[len(userMemories)-5:]
	}
	return ResumeContext{
		SessionID:      sess.ID,
		WorkspaceRoot:  sess.WorkspaceRoot,
		LastInput:      turn.Input,
		Memory:         userMemories,
		ResumeRequired: turn.AwaitingApproval || strings.TrimSpace(turn.Input) != "",
	}, nil
}

type ResumeContext struct {
	SessionID      string
	WorkspaceRoot  string
	LastInput      string
	Memory         []string
	ResumeRequired bool
}
