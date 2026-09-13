// Package cli owns command parsing and the Manager-backed client entry point.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"eino-cli/backend/cli/tui"
	host "eino-cli/host/runtime"
	"eino-cli/manager"
	"eino-cli/manager/api"
	"eino-cli/protocol"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

type Options struct {
	Root, ConfigPath, SessionID, ThreadID, Prompt string
	Plan, JSON                                    bool
}

func Parse(args []string) (Options, error) {
	var o Options
	f := flag.NewFlagSet("deepagent", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&o.Root, "root", "", "workspace root (default SGADK_ROOT or current directory)")
	f.StringVar(&o.ConfigPath, "config", "", "shared storage configuration YAML")
	f.StringVar(&o.SessionID, "session", "", "session ID for related threads")
	f.StringVar(&o.ThreadID, "thread", "", "attach existing shared thread")
	f.StringVar(&o.Prompt, "prompt", "", "one-shot prompt; - reads stdin")
	f.BoolVar(&o.Plan, "plan", false, "enable plan mode")
	f.BoolVar(&o.JSON, "json", false, "emit protocol events as JSONL")
	if err := f.Parse(args); err != nil {
		return o, err
	}
	if len(f.Args()) > 0 {
		if o.Prompt != "" {
			return o, fmt.Errorf("use --prompt or positional prompt, not both")
		}
		o.Prompt = strings.Join(f.Args(), " ")
	}
	if strings.TrimSpace(o.Root) == "" {
		o.Root = os.Getenv("SGADK_ROOT")
	}
	if o.Root == "" {
		var err error
		o.Root, err = os.Getwd()
		if err != nil {
			return o, err
		}
	}
	var err error
	o.Root, err = filepath.Abs(o.Root)
	if err != nil {
		return o, err
	}
	if o.ConfigPath == "" {
		o.ConfigPath = filepath.Join(o.Root, "yaml", "deepagent.yaml")
	}
	return o, nil
}
func LoadManagerConfig(path string) (manager.Config, error) {
	var raw struct {
		Manager struct {
			Namespace     string `yaml:"namespace"`
			MySQLDSN      string `yaml:"mysql_dsn"`
			RedisAddr     string `yaml:"redis_addr"`
			RedisPassword string `yaml:"redis_password"`
			RedisDB       int    `yaml:"redis_db"`
		} `yaml:"manager"`
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return manager.Config{}, fmt.Errorf("read storage config: %w (copy yaml/deepagent.example.yaml)", err)
	}
	if err = yaml.Unmarshal(data, &raw); err != nil {
		return manager.Config{}, fmt.Errorf("decode storage config: %w", err)
	}
	m := raw.Manager
	cfg := manager.Config{Namespace: os.ExpandEnv(m.Namespace), MySQLDSN: os.ExpandEnv(m.MySQLDSN), RedisAddr: os.ExpandEnv(m.RedisAddr), RedisPassword: os.ExpandEnv(m.RedisPassword), RedisDB: m.RedisDB}
	if cfg.Namespace == "" {
		cfg.Namespace = "default"
	}
	if cfg.RedisAddr == "" {
		cfg.RedisAddr = "127.0.0.1:6379"
	}
	if cfg.MySQLDSN == "" {
		return cfg, fmt.Errorf("manager.mysql_dsn is required")
	}
	return cfg, nil
}
func Run(args []string) error {
	o, err := Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Println("deepagent [--root DIR] [--config YAML] [--session ID] [--thread ID] [--plan] [--json] [--prompt TEXT|-]\nStart deepagent_worker separately. Models and tools run only in Workers.")
		return nil
	}
	if err != nil {
		return err
	}
	if err = os.Chdir(o.Root); err != nil {
		return err
	}
	if o.Prompt == "-" || (o.Prompt == "" && !term.IsTerminal(int(os.Stdin.Fd()))) {
		data, e := io.ReadAll(io.LimitReader(os.Stdin, 8<<20))
		if e != nil {
			return e
		}
		o.Prompt = string(data)
	}
	cfg, err := LoadManagerConfig(o.ConfigPath)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	m, err := manager.New(ctx, cfg)
	if err != nil {
		return err
	}
	defer m.Close()
	if err := attachThread(ctx, m, &o); err != nil {
		return err
	}
	rt := host.New(m, host.Config{SessionID: o.SessionID, ThreadID: o.ThreadID, WorkDir: o.Root, PlanMode: o.Plan})
	defer rt.Detach()
	detachSignal := context.AfterFunc(ctx, rt.Detach)
	defer detachSignal()
	if strings.TrimSpace(o.Prompt) == "" {
		return tui.Run(rt, rt.SessionID())
	}
	enc := json.NewEncoder(os.Stdout)
	printed := map[string]string{}
	result, err := rt.ExecuteEvents(ctx, o.Prompt, func(e protocol.Event) {
		if o.JSON {
			_ = enc.Encode(e)
		} else if e.Kind == protocol.EventTextDelta {
			fmt.Print(e.Text)
			printed[e.ResponseID] += e.Text
		} else if e.Kind == protocol.EventText {
			before := printed[e.ResponseID]
			if strings.HasPrefix(e.Text, before) {
				fmt.Print(strings.TrimPrefix(e.Text, before))
			} else {
				fmt.Print("\n" + e.Text)
			}
			printed[e.ResponseID] = e.Text
		}
	})
	fmt.Fprintf(os.Stderr, "\nsession=%s thread=%s\n", rt.SessionID(), rt.ThreadID())
	if err != nil {
		if ctx.Err() != nil {
			cancelCtx, cancel := context.WithTimeout(context.Background(), 5e9)
			defer cancel()
			_ = rt.Cancel(cancelCtx)
		}
		return err
	}
	if !o.JSON {
		fmt.Println()
		if result.NeedsUser {
			fmt.Println(result.Output)
		}
	}
	if result.NeedsUser {
		return fmt.Errorf("thread is waiting for input; resume with --thread %s --prompt YOUR_ANSWER", rt.ThreadID())
	}
	return nil
}

func attachThread(ctx context.Context, m api.Manager, o *Options) error {
	if o.ThreadID != "" {
		t, e := m.GetThread(ctx, o.ThreadID)
		if e != nil {
			return e
		}
		if o.SessionID != "" && o.SessionID != t.SessionID {
			return fmt.Errorf("thread does not belong to requested session")
		}
		o.SessionID = t.SessionID
		if o.Plan {
			if err := m.SetPlanMode(ctx, t.ID, true); err != nil {
				return err
			}
		}
	}
	return nil
}
