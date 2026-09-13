// Package backends provides rooted filesystem tools and an approval-gated host command tool.
// Commands run on the host in Root; this is not a container or OS security sandbox.
package filesystem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"io"
	iofs "io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Filesystem struct{ Root string }

func NewFilesystem(root string) (*Filesystem, error) {
	p, e := filepath.Abs(root)
	if e != nil {
		return nil, e
	}
	r, e := os.OpenRoot(p)
	if e != nil {
		return nil, e
	}
	r.Close()
	return &Filesystem{Root: p}, nil
}
func (f *Filesystem) Tools() []tool.BaseTool {
	return []tool.BaseTool{&fileTool{f, "read_file"}, &fileTool{f, "list_files"}, &fileTool{f, "write_file"}, &fileTool{f, "edit_file"}, &fileTool{f, "search_files"}, &planTool{}, &commandTool{f}, &clarifyTool{}}
}

type fileTool struct {
	fs   *Filesystem
	name string
}

func (f *fileTool) ReadOnly() bool         { return f.name != "write_file" && f.name != "edit_file" }
func (f *fileTool) RequiresApproval() bool { return !f.ReadOnly() }
func (f *fileTool) Info(context.Context) (*schema.ToolInfo, error) {
	params := map[string]*schema.ParameterInfo{"path": {Type: schema.String, Desc: "Path relative to the thread working directory", Required: true}}
	if f.name == "search_files" {
		params["query"] = &schema.ParameterInfo{Type: schema.String, Required: true}
	}
	if f.name == "edit_file" {
		params["old"] = &schema.ParameterInfo{Type: schema.String, Required: true}
		params["new"] = &schema.ParameterInfo{Type: schema.String, Required: true}
	}
	if f.name == "write_file" {
		params["content"] = &schema.ParameterInfo{Type: schema.String, Required: true}
	}
	return &schema.ToolInfo{Name: f.name, Desc: map[string]string{"search_files": "Recursively search literal text in UTF-8 files (at most 100 matches)", "edit_file": "Replace exactly one matching text span in a UTF-8 file; requires approval", "read_file": "Read UTF-8 file (up to 1 MiB)", "list_files": "List a directory", "write_file": "Write a UTF-8 file; requires approval"}[f.name], ParamsOneOf: schema.NewParamsOneOfByParams(params)}, nil
}
func (f *fileTool) InvokableRun(ctx context.Context, arg string, _ ...tool.Option) (string, error) {
	if e := ctx.Err(); e != nil {
		return "", e
	}
	var in struct{ Path, Content, Query, Old, New string }
	if e := json.Unmarshal([]byte(arg), &in); e != nil {
		return "", e
	}
	if in.Path == "" {
		in.Path = "."
	}
	if filepath.IsAbs(in.Path) {
		return "", errors.New("path must be relative to working directory")
	}
	r, e := os.OpenRoot(f.fs.Root)
	if e != nil {
		return "", e
	}
	defer r.Close()
	switch f.name {
	case "search_files":
		if in.Query == "" {
			return "", errors.New("query required")
		}
		var matches []string
		visited := 0
		e = iofs.WalkDir(r.FS(), in.Path, func(path string, d iofs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if err = ctx.Err(); err != nil {
				return err
			}
			visited++
			if visited > 10000 || len(matches) >= 100 {
				return iofs.SkipAll
			}
			if d.IsDir() || d.Type()&os.ModeSymlink != 0 {
				return nil
			}
			file, err := r.Open(path)
			if err != nil {
				return err
			}
			b, err := io.ReadAll(io.LimitReader(file, 1<<20))
			file.Close()
			if err != nil {
				return err
			}
			if strings.ContainsRune(string(b), 0) {
				return nil
			}
			for i, line := range strings.Split(string(b), "\n") {
				if strings.Contains(line, in.Query) {
					matches = append(matches, fmt.Sprintf("%s:%d:%s", path, i+1, line))
					if len(matches) >= 100 {
						break
					}
				}
			}
			return nil
		})
		return strings.Join(matches, "\n"), e
	case "edit_file":
		if in.Old == "" {
			return "", errors.New("old text required")
		}
		file, err := r.Open(in.Path)
		if err != nil {
			return "", err
		}
		b, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
		file.Close()
		if err != nil {
			return "", err
		}
		if len(b) > 1<<20 {
			return "", errors.New("file exceeds 1 MiB")
		}
		if strings.Count(string(b), in.Old) != 1 {
			return "", errors.New("old text must match exactly once")
		}
		out := strings.Replace(string(b), in.Old, in.New, 1)
		file, err = r.OpenFile(in.Path, os.O_WRONLY|os.O_TRUNC, 0600)
		if err != nil {
			return "", err
		}
		_, err = file.WriteString(out)
		closeErr := file.Close()
		if err != nil {
			return "", err
		}
		return "File edited.", closeErr

	case "write_file":
		file, e := r.OpenFile(in.Path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if e != nil {
			return "", e
		}
		_, e = file.WriteString(in.Content)
		ce := file.Close()
		if e != nil {
			return "", e
		}
		if ce != nil {
			return "", ce
		}
		return "File written.", nil
	case "read_file":
		file, e := r.Open(in.Path)
		if e != nil {
			return "", e
		}
		defer file.Close()
		b, e := io.ReadAll(io.LimitReader(file, 1<<20+1))
		if len(b) > 1<<20 {
			return "", errors.New("file exceeds 1 MiB; narrow the input")
		}
		return string(b), e
	case "list_files":
		file, e := r.Open(in.Path)
		if e != nil {
			return "", e
		}
		defer file.Close()
		entries, e := file.ReadDir(1001)
		if e != nil && !errors.Is(e, io.EOF) {
			return "", e
		}
		if len(entries) > 1000 {
			return "", errors.New("directory exceeds 1000 entries")
		}
		names := make([]string, len(entries))
		for i, en := range entries {
			names[i] = en.Name()
			if en.IsDir() {
				names[i] += "/"
			}
		}
		return strings.Join(names, "\n"), nil
	}
	return "", errors.New("unknown filesystem operation")
}

type commandTool struct{ fs *Filesystem }

func (c *commandTool) ReadOnly() bool         { return false }
func (c *commandTool) RequiresApproval() bool { return true }
func (c *commandTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "execute", Desc: "Run a shell command on the host in the working directory after explicit approval. This is not an OS sandbox.", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"command": {Type: schema.String, Required: true}, "timeout_seconds": {Type: schema.Integer, Desc: "Timeout (1-300 seconds, default 60)"}})}, nil
}
func (c *commandTool) StreamableRun(ctx context.Context, arg string, _ ...tool.Option) (*schema.StreamReader[string], error) {
	var in struct {
		Command string `json:"command"`
		Timeout int    `json:"timeout_seconds"`
	}
	if e := json.Unmarshal([]byte(arg), &in); e != nil {
		return nil, e
	}
	if strings.TrimSpace(in.Command) == "" {
		return nil, errors.New("command required")
	}
	if in.Timeout <= 0 {
		in.Timeout = 60
	}
	if in.Timeout > 300 {
		in.Timeout = 300
	}
	r, w := schema.Pipe[string](16)
	go func() {
		defer w.Close()
		ctx, cancel := context.WithTimeout(ctx, time.Duration(in.Timeout)*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", in.Command)
		cmd.Dir = c.fs.Root
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			if cmd.Process == nil {
				return nil
			}
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		cmd.WaitDelay = time.Second
		output := &streamOutput{w: w, limit: 1 << 20}
		cmd.Stdout = output
		cmd.Stderr = output
		if e := cmd.Run(); e != nil {
			w.Send("", fmt.Errorf("command: %w", e))
		}
	}()
	return r, nil
}

type streamOutput struct {
	mu    sync.Mutex
	w     *schema.StreamWriter[string]
	limit int
}

func (s *streamOutput) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(p)
	if s.limit <= 0 {
		return n, nil
	}
	if len(p) > s.limit {
		p = p[:s.limit]
	}
	s.limit -= len(p)
	if closed := s.w.Send(string(p), nil); closed {
		return 0, io.ErrClosedPipe
	}
	return n, nil
}

type clarifyTool struct{}

func (*clarifyTool) ReadOnly() bool { return true }
func (*clarifyTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "ask_user", Desc: "Pause and ask the user a question; execution resumes with their answer.", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"question": {Type: schema.String, Required: true}, "options": {Type: schema.Array, ElemInfo: &schema.ParameterInfo{Type: schema.String}, Desc: "Optional concise choices for the user"}})}, nil
}
func (*clarifyTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return "", errors.New("ask_user must execute through the Core interrupt gate")
}

type planTool struct{}

func (*planTool) ReadOnly() bool { return true }

func (*planTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "update_plan", Desc: "Publish plan steps and their progress for the user.", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"todos": {Type: schema.Array, ElemInfo: &schema.ParameterInfo{Type: schema.Object, SubParams: map[string]*schema.ParameterInfo{"content": {Type: schema.String, Required: true}, "status": {Type: schema.String, Enum: []string{"pending", "in_progress", "completed"}, Required: true}}}}, "plan": {Type: schema.String, Desc: "Legacy plain-text plan, used when todos is absent"}})}, nil
}
func (*planTool) InvokableRun(_ context.Context, arg string, _ ...tool.Option) (string, error) {
	var in struct {
		Plan  string
		Todos []struct {
			Content string `json:"content"`
			Status  string `json:"status"`
		}
	}
	if e := json.Unmarshal([]byte(arg), &in); e != nil {
		return "", e
	}
	if len(in.Todos) == 0 {
		if strings.TrimSpace(in.Plan) == "" {
			return "", errors.New("todos or plan required")
		}
		out, _ := json.Marshal(map[string]any{"todos": []map[string]string{{"content": in.Plan, "status": "in_progress"}}})
		return string(out), nil
	}
	for _, todo := range in.Todos {
		if strings.TrimSpace(todo.Content) == "" || (todo.Status != "pending" && todo.Status != "in_progress" && todo.Status != "completed") {
			return "", errors.New("invalid todo content/status")
		}
	}
	out, _ := json.Marshal(map[string]any{"todos": in.Todos})
	return string(out), nil
}
