package tracker

import "eino-cli/backend/task"

type Tracker struct {
	tasks []task.Task
}

func New(tasks []task.Task) *Tracker {
	copied := append([]task.Task(nil), tasks...)
	return &Tracker{tasks: copied}
}

func (t *Tracker) Tasks() []task.Task {
	return append([]task.Task(nil), t.tasks...)
}

func (t *Tracker) SetStatus(id string, status task.Status) {
	for i := range t.tasks {
		if t.tasks[i].ID == id {
			t.tasks[i].Status = status
			return
		}
	}
}
