package deepagents

import (
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/compose"
)

// RunOptions 运行时选项。
type RunOptions struct {
	composeOpts         []compose.Option
	CheckpointID        string
	WriteToCheckpointID string
	ForceNewRun         bool
	ResumeInterruptIDs  []string
	ResumeData          map[string]any
}

// RunOptionFunc 运行时选项函数。
type RunOptionFunc func(*RunOptions)

// WithCallbacks 设置回调。
func WithCallbacks(cbs ...callbacks.Handler) RunOptionFunc {
	return func(o *RunOptions) { o.composeOpts = append(o.composeOpts, compose.WithCallbacks(cbs...)) }
}

// WithCheckpointID 设置 checkpoint ID（用于读取和写入）。
func WithCheckpointID(id string) RunOptionFunc {
	return func(o *RunOptions) { o.CheckpointID = id }
}

// WithWriteToCheckpointID 设置写入到不同的 checkpoint ID。
func WithWriteToCheckpointID(id string) RunOptionFunc {
	return func(o *RunOptions) { o.WriteToCheckpointID = id }
}

// WithForceNewRun 强制从头开始运行，忽略已有 checkpoint。
func WithForceNewRun() RunOptionFunc {
	return func(o *RunOptions) { o.ForceNewRun = true }
}

// WithResume 恢复指定的中断点。
func WithResume(interruptIDs ...string) RunOptionFunc {
	return func(o *RunOptions) { o.ResumeInterruptIDs = append(o.ResumeInterruptIDs, interruptIDs...) }
}

// WithResumeData 带数据恢复中断点，data 的 key 为中断点 ID。
func WithResumeData(data map[string]any) RunOptionFunc {
	return func(o *RunOptions) {
		if o.ResumeData == nil {
			o.ResumeData = make(map[string]any)
		}
		for k, v := range data {
			o.ResumeData[k] = v
		}
	}
}

func applyRunOptions(opts []RunOptionFunc) *RunOptions {
	ro := &RunOptions{}
	for _, opt := range opts {
		opt(ro)
	}
	return ro
}
