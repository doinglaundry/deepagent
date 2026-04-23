package orchestrator

type Orchestrator struct {
	mode Mode
}

func New() *Orchestrator {
	return &Orchestrator{mode: ModeSingleAgent}
}
