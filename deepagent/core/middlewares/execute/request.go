package execute

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	DefaultWorkDir         = "."
	DefaultCommandTimeout  = 30 * time.Second
	MaxCommandTimeout      = 10 * time.Minute
	DefaultMaxOutputTokens = 4096
	MaxOutputTokens        = 16384
)

// ExecCommandInput is the untrusted parameter object produced by a model.
// Timeout is expressed in milliseconds at the tool boundary.
type ExecCommandInput struct {
	Cmd             string            `json:"cmd"`
	WorkDir         string            `json:"workdir"`
	Timeout         int               `json:"timeout"`
	MaxOutputTokens int               `json:"max_output_tokens"`
	Justification   string            `json:"justification"`
	Env             map[string]string `json:"env"`
}

// ExecRequest is the normalized request consumed by execution policy code.
type ExecRequest struct {
	Command         string
	WorkDir         string
	Timeout         time.Duration
	MaxOutputTokens int
	Justification   string
	Env             map[string]string
}

// CommandSpec converts a normalized request into the shared execution
// description. The command is intentionally kept as raw text so the policy
// classifier can inspect shell operators before tokenization.
func (r ExecRequest) CommandSpec() CommandSpec {
	return CommandSpec{
		Args:          tokenizeShellWords(r.Command),
		Command:       r.Command,
		RawCommand:    r.Command,
		WorkDir:       r.WorkDir,
		Timeout:       r.Timeout,
		Justification: r.Justification,
		Env:           cloneEnv(r.Env),
	}
}

// NormalizeRequest validates and normalizes model supplied execution input.
func NormalizeRequest(input ExecCommandInput) (ExecRequest, error) {
	return NormalizeRequestWithBase(input, "")
}

// NormalizeRequestWithBase normalizes an execution input using base as the
// configured default working directory.
func NormalizeRequestWithBase(input ExecCommandInput, base string) (ExecRequest, error) {
	command := strings.TrimSpace(input.Cmd)
	if command == "" {
		return ExecRequest{}, errors.New("command is required")
	}
	workDir, err := normalizeWorkDir(input.WorkDir, base)
	if err != nil {
		return ExecRequest{}, err
	}
	timeout := DefaultCommandTimeout
	if input.Timeout > 0 {
		timeout = time.Duration(input.Timeout) * time.Millisecond
	}
	if timeout > MaxCommandTimeout {
		timeout = MaxCommandTimeout
	}
	maxTokens := input.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = DefaultMaxOutputTokens
	}
	if maxTokens > MaxOutputTokens {
		maxTokens = MaxOutputTokens
	}
	return ExecRequest{
		Command:         command,
		WorkDir:         workDir,
		Timeout:         timeout,
		MaxOutputTokens: maxTokens,
		Justification:   strings.TrimSpace(input.Justification),
		Env:             cloneEnv(input.Env),
	}, nil
}

func normalizeWorkDir(raw, configuredBase string) (string, error) {
	raw = strings.TrimSpace(raw)
	base := strings.TrimSpace(configuredBase)
	if base == "" {
		var err error
		base, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	if raw == "" {
		return filepath.Clean(base), nil
	}
	if filepath.IsAbs(raw) {
		return filepath.Clean(raw), nil
	}
	return filepath.Clean(filepath.Join(base, raw)), nil
}

func cloneEnv(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[strings.TrimSpace(k)] = v
	}
	return out
}
