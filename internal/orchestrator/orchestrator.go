package orchestrator

import (
	memorypolicy "eino-cli/internal/memory/policy"
	memorystore "eino-cli/internal/memory/store"
	"eino-cli/internal/session"
	"eino-cli/internal/session/checkpoint"
	"eino-cli/internal/task"
)

type Persistence struct {
	SessionStore    *session.Store
	CheckpointStore *checkpoint.Store
	MemoryStore     *memorystore.Store
	MemoryPolicy    *memorypolicy.Policy
}

func NewPersistence(sessionStore *session.Store, checkpointStore *checkpoint.Store, memoryStore *memorystore.Store, memoryPolicy *memorypolicy.Policy) *Persistence {
	return &Persistence{
		SessionStore:    sessionStore,
		CheckpointStore: checkpointStore,
		MemoryStore:     memoryStore,
		MemoryPolicy:    memoryPolicy,
	}
}

func (p *Persistence) SaveSession(sess session.Session) error {
	if p == nil || p.SessionStore == nil {
		return nil
	}
	return p.SessionStore.Save(sess)
}

func (p *Persistence) SaveCheckpoint(snapshot checkpoint.Snapshot) error {
	if p == nil || p.CheckpointStore == nil {
		return nil
	}
	return p.CheckpointStore.Save(snapshot)
}

func (p *Persistence) SaveTask(_ session.Session, _ task.Task) error {
	return nil
}
