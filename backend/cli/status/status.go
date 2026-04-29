package status

import "fmt"

type Snapshot struct {
	Workspace string
	Mode      string
	TaskState string
	Warning   string
}

func (s Snapshot) String() string {
	if s.Warning == "" {
		return fmt.Sprintf("workspace=%s mode=%s task=%s", s.Workspace, s.Mode, s.TaskState)
	}
	return fmt.Sprintf("workspace=%s mode=%s task=%s warning=%s", s.Workspace, s.Mode, s.TaskState, s.Warning)
}
