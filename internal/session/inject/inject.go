package inject

import (
	"strings"

	memoryretrieval "eino-cli/internal/memory/retrieval"
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

func Build(sess session.Session, snapshot checkpoint.Snapshot, retriever *memoryretrieval.Retriever) (Context, error) {
	memories, err := retriever.Find("")
	if err != nil {
		return Context{}, err
	}
	items := make([]string, 0, len(memories))
	for _, memory := range memories {
		items = append(items, memory.Content)
	}
	return Context{
		SessionID:      sess.ID,
		WorkspaceRoot:  sess.WorkspaceRoot,
		LastInput:      snapshot.LastInput,
		Memory:         items,
		ResumeRequired: snapshot.AwaitingApproval || strings.TrimSpace(snapshot.LastInput) != "",
	}, nil
}
