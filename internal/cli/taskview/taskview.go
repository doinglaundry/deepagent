package taskview

import (
	"fmt"
	"strings"

	"eino-cli/internal/task"
)

type Item struct {
	Title  string
	Status string
}

type View struct {
	Items []Item
}

func FromTasks(tasks []task.Task) View {
	items := make([]Item, 0, len(tasks))
	for _, current := range tasks {
		items = append(items, Item{Title: current.Title, Status: string(current.Status)})
	}
	return View{Items: items}
}

func (v View) String() string {
	if len(v.Items) == 0 {
		return "tasks: none"
	}
	lines := make([]string, 0, len(v.Items)+1)
	lines = append(lines, "tasks:")
	for _, item := range v.Items {
		lines = append(lines, fmt.Sprintf("- [%s] %s", item.Status, item.Title))
	}
	return strings.Join(lines, "\n")
}
