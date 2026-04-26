package inject

import (
	"strings"

	memoryretrieval "eino-cli/internal/memory/retrieval"
	memorystore "eino-cli/internal/memory/store"
	"eino-cli/internal/session"
	"eino-cli/internal/session/checkpoint"
)

type Context struct {
	SessionID      string
	WorkspaceRoot  string
	LastInput      string
	Memory         []string
	ResumeRequired bool
}

const maxMemoryContext = 5

func Build(sess session.Session, snapshot checkpoint.Snapshot, retriever *memoryretrieval.Retriever) (Context, error) {
	memories, err := retriever.Find("")
	if err != nil {
		return Context{}, err
	}
	var userMemories []string
	for _, m := range memories {
		if !strings.HasPrefix(m.Content, memorystore.TaskMemoryPrefix) {
			userMemories = append(userMemories, m.Content)
		}
	}
	if len(userMemories) > maxMemoryContext {
		userMemories = userMemories[len(userMemories)-maxMemoryContext:]
	}
	return Context{
		SessionID:      sess.ID,
		WorkspaceRoot:  sess.WorkspaceRoot,
		LastInput:      snapshot.LastInput,
		Memory:         userMemories,
		ResumeRequired: snapshot.AwaitingApproval || strings.TrimSpace(snapshot.LastInput) != "",
	}, nil
}
