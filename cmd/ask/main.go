// Command eino-ask is a non-interactive smoke-test driver: load
// config, build the runtime, send one prompt, print the answer.
//
// This is the same wiring the TUI uses (config.Load ->
// eino.BuildRuntime -> ExecuteStream), minus the bubbletea
// front-end. Useful for verifying API-key plumbing without a TTY
// and for shell pipelines (`echo "explain X" | eino-ask`).
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"eino-cli/backend/config"
	"eino-cli/backend/runtime/eino"
)

func main() {
	stream := flag.Bool("stream", true, "stream chunks to stdout as they arrive")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `usage: eino-ask [flags] [prompt...]

If no prompt is provided as args, reads stdin until EOF.
Examples:
  eino-ask "what is 2+2"
  echo "ping" | eino-ask
  eino-ask -stream=false "summarise X"

Flags:
`)
		flag.PrintDefaults()
	}
	flag.Parse()

	prompt := strings.TrimSpace(strings.Join(flag.Args(), " "))
	if prompt == "" {
		buf, err := io.ReadAll(os.Stdin)
		if err != nil {
			die("read stdin: %v", err)
		}
		prompt = strings.TrimSpace(string(buf))
	}
	if prompt == "" {
		flag.Usage()
		os.Exit(2)
	}

	cfg, err := config.Load()
	if err != nil {
		die("load config: %v", err)
	}

	ctx := context.Background()
	rt, err := eino.BuildRuntime(ctx, cfg)
	if err != nil {
		die("build runtime: %v", err)
	}

	fmt.Fprintf(os.Stderr, "→ model: %s\n→ prompt: %s\n\n", rt.Name(), trimForLog(prompt))

	var onChunk eino.StreamChunkHandler
	if *stream {
		onChunk = func(chunk string) {
			fmt.Fprint(os.Stdout, chunk)
		}
	}

	result, err := rt.ExecuteStream(ctx, prompt, onChunk)
	if err != nil {
		die("execute: %v", err)
	}

	if !*stream {
		// In non-stream mode the chunk callback was nil; print
		// the runtime's collated final output.
		fmt.Println(result.Output)
	} else if result.Output != "" && !strings.HasSuffix(result.Output, "\n") {
		fmt.Println()
	}

	if !result.Success {
		die("runtime returned non-success: code=%s msg=%s", result.Code, result.Message)
	}
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}

func trimForLog(s string) string {
	const max = 120
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
