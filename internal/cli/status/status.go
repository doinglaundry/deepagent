package status

import "fmt"

type Snapshot struct {
	Workspace string
	Mode      string
	TaskState string
}

func (s Snapshot) String() string {
	return fmt.Sprintf("workspace=%s mode=%s task=%s", s.Workspace, s.Mode, s.TaskState)
}
