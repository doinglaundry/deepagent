// Package cli is the thin terminal client for the canonical distributed Manager.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	dalmodel "eino-cli/deepagent/dal/model"
	deepmanager "eino-cli/deepagent/manager"
	eventpkg "eino-cli/deepagent/protocol/event"
	inputpkg "eino-cli/deepagent/protocol/input"
	"github.com/google/uuid"
)

type Options struct {
	Root, ConfigPath, SessionID, ThreadID, Prompt string
	Plan, JSON                                    bool
}

func Parse(args []string) (Options, error) {
	var o Options
	f := flag.NewFlagSet("deepagent", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&o.Root, "root", "", "workspace root")
	f.StringVar(&o.ConfigPath, "config", "", "shared storage configuration YAML")
	f.StringVar(&o.SessionID, "session", "", "session ID for related threads")
	f.StringVar(&o.ThreadID, "thread", "", "attach existing shared thread")
	f.StringVar(&o.Prompt, "prompt", "", "one-shot prompt; - reads stdin")
	f.BoolVar(&o.Plan, "plan", false, "enable plan mode")
	f.BoolVar(&o.JSON, "json", false, "emit events as JSONL")
	if err := f.Parse(args); err != nil {
		return o, err
	}
	if len(f.Args()) > 0 {
		if o.Prompt != "" {
			return o, errors.New("use --prompt or positional prompt, not both")
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

func LoadManagerConfig(path string) (deepmanager.Config, error) {
	return deepmanager.LoadConfig(path)
}

func Run(args []string) error {
	o, err := Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Println("deepagent [--root DIR] [--config YAML] [--session ID] [--thread ID] [--plan] [--json] [--prompt TEXT|-]")
		return nil
	}
	if err != nil {
		return err
	}
	if o.Prompt == "-" {
		data, readErr := io.ReadAll(io.LimitReader(os.Stdin, 8<<20))
		if readErr != nil {
			return readErr
		}
		o.Prompt = string(data)
	}
	cfg, err := LoadManagerConfig(o.ConfigPath)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	coordinator, err := deepmanager.Open(ctx, cfg)
	if err != nil {
		return err
	}
	if strings.TrimSpace(o.Prompt) != "" {
		_, err = execute(ctx, coordinator, &o, o.Prompt)
		return err
	}
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			return scanner.Err()
		}
		prompt := strings.TrimSpace(scanner.Text())
		if prompt == "" {
			continue
		}
		if _, err = execute(ctx, coordinator, &o, prompt); err != nil {
			return err
		}
	}
}

func execute(ctx context.Context, coordinator *deepmanager.Manager, options *Options, prompt string) (int64, error) {
	threadID, err := parseThreadID(options.ThreadID)
	if err != nil {
		return 0, err
	}
	if threadID != 0 {
		found, getErr := coordinator.ListThreads(ctx, deepmanager.ListThreadsRequest{ThreadID: threadID})
		if getErr != nil {
			return 0, getErr
		}
		if options.SessionID != "" && options.SessionID != found.Thread.SessionID {
			return 0, errors.New("thread does not belong to requested session")
		}
		options.SessionID = found.Thread.SessionID
		if found.Thread.Status == dalmodel.ThreadStatusBlocked {
			return 0, errors.New("thread is blocked; resume it through the structured UI")
		}
	}
	if options.SessionID == "" {
		options.SessionID = uuid.NewString()
	}
	mode := inputpkg.UserMessageMode("")
	if options.Plan {
		mode = inputpkg.UserMessageModeImplPlan
	}
	payload, err := json.Marshal(inputpkg.UserMessage{
		Mode:  mode,
		Parts: []inputpkg.MessagePart{{Type: inputpkg.MessagePartTypeText, Text: prompt}},
	})
	if err != nil {
		return 0, err
	}
	result, err := coordinator.Submit(ctx, deepmanager.SubmitRequest{
		ThreadID: threadID, SessionID: options.SessionID,
		Profile: &dalmodel.Profile{Cwd: options.Root},
		Input:   &deepmanager.InputMessage{SenderType: "user", MessageType: inputpkg.MessageTypeInput, Payload: payload},
	})
	if err != nil {
		return 0, err
	}
	threadID = result.Thread.ThreadID
	options.ThreadID = strconv.FormatInt(threadID, 10)
	subscription, err := coordinator.SubscribeSession(ctx, options.SessionID, "")
	if err != nil {
		return 0, err
	}
	defer subscription.Close()
	encoder := json.NewEncoder(os.Stdout)
	for {
		select {
		case <-ctx.Done():
			_, _ = coordinator.Cancel(context.Background(), threadID, "client canceled", nil)
			return threadID, ctx.Err()
		case frame, ok := <-subscription.Events:
			if !ok {
				return threadID, errors.New("event subscription closed")
			}
			if frame.ThreadID != threadID {
				continue
			}
			if options.JSON {
				_ = encoder.Encode(frame)
			}
			done, wait, eventErr := printFrame(frame, options.JSON)
			if eventErr != nil {
				return threadID, eventErr
			}
			if wait {
				return threadID, fmt.Errorf("thread %d is waiting for structured input", threadID)
			}
			if done {
				if !options.JSON {
					fmt.Println()
				}
				return threadID, nil
			}
		}
	}
}

func printFrame(frame deepmanager.OutputFrame, quiet bool) (done, waiting bool, err error) {
	switch eventpkg.Type(frame.EventType) {
	case eventpkg.EventTypeAssistantDelta:
		if quiet {
			return false, false, nil
		}
		var payload eventpkg.AssistantDeltaEventPayload
		if err = json.Unmarshal(frame.Payload, &payload); err == nil {
			fmt.Print(payload.Delta)
		}
	case eventpkg.EventTypeAssistantMessage:
		if quiet {
			return false, false, nil
		}
		var payload eventpkg.MessageEventPayload
		if err = json.Unmarshal(frame.Payload, &payload); err == nil {
			for _, part := range payload.Parts {
				fmt.Print(part.Text)
			}
		}
	case eventpkg.EventTypeInputRequired:
		return false, true, nil
	case eventpkg.EventTypeError:
		var payload eventpkg.ErrorEventPayload
		if err = json.Unmarshal(frame.Payload, &payload); err == nil {
			err = errors.New(payload.Message)
		}
	case eventpkg.EventTypeRunStatus:
		var payload struct{ Status string }
		if err = json.Unmarshal(frame.Payload, &payload); err == nil {
			return payload.Status == eventpkg.RunStatusFinished, false, nil
		}
	}
	return false, false, err
}

func parseThreadID(raw string) (int64, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("thread must be a positive integer")
	}
	return id, nil
}
