package planner

import "eino-cli/internal/task"

type Planner struct{}

func New() *Planner {
	return &Planner{}
}

func (p *Planner) Plan(input string) []task.Task {
	if input == "" {
		return nil
	}
	return []task.Task{task.New("task-1", input)}
}
