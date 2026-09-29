package backend

import "context"

type Filesystem interface {
	Root() string
	Resolve(context.Context, string, bool) (string, error)
	List(context.Context, string) ([]FileInfo, error)
	Read(context.Context, string, *int, *int) (string, error)
	Write(context.Context, string, string) (*WriteResult, error)
	Edit(context.Context, string, string, string, bool) (*EditResult, error)
	Delete(context.Context, string) (string, error)
	Glob(context.Context, string, string) ([]FileInfo, error)
	Grep(context.Context, string, string, string) ([]GrepMatch, error)
	ApplyPatch(context.Context, string) (string, error)
}

// patchFilesystem provides the operations required to validate and commit a
// patch without treating a content read as an existence check.
type patchFilesystem interface {
	Filesystem
	FileExists(context.Context, string) (bool, error)
	CreateFileNoReplace(context.Context, string, string) (*WriteResult, error)
}

// ToolFilesystem is the complete capability set exposed to one Agent thread.
// Both local and Docker filesystems implement it; tools use the same interface.
type ToolFilesystem interface {
	Filesystem
	CommandService
}
