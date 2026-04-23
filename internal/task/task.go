package task

type Status string

const (
	StatusPending    Status = "pending"
	StatusInProgress Status = "in_progress"
	StatusCompleted  Status = "completed"
	StatusBlocked    Status = "blocked"
	StatusFailed     Status = "failed"
)

type Task struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status Status `json:"status"`
}

func New(id, title string) Task {
	return Task{ID: id, Title: title, Status: StatusPending}
}
