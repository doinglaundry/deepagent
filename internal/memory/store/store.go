package store

type Memory struct {
	Content string
}

func New(content string) Memory {
	return Memory{Content: content}
}
