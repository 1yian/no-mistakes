package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	os.Exit(run())
}

func run() int {
	if len(os.Args) < 2 {
		usage()
		return 2
	}
	agent := os.Args[1]
	args := os.Args[2:]

	out, args, err := parseOut(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := os.MkdirAll(out, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir %s: %v\n", out, err)
		return 1
	}

	switch agent {
	case "claude":
		return recordClaude(ctx, out, args)
	case "codex":
		return recordCodex(ctx, out, args)
	case "opencode":
		return recordOpencode(ctx, out, args)
	case "antigravity":
		return recordAntigravity(ctx, out, args)
	default:
		fmt.Fprintf(os.Stderr, "unknown agent %q (want claude|codex|opencode|antigravity)\n", agent)
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: recordfixture <claude|codex|opencode|antigravity> --out <dir> [--bin <path>]")
	fmt.Fprintln(os.Stderr, "captures real agent output as e2e fixture files. burns real API quota.")
}

func splitBinArgs(args []string, def string) (string, []string) {
	bin := def
	forwarded := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--bin" && i+1 < len(args) {
			bin = args[i+1]
			i++
			continue
		}
		forwarded = append(forwarded, args[i])
	}
	return bin, forwarded
}

func parseOut(args []string) (string, []string, error) {
	out := ""
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--out":
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("--out: missing value")
			}
			out = args[i+1]
			i++
		default:
			rest = append(rest, args[i])
		}
	}
	if out == "" {
		return "", nil, fmt.Errorf("--out is required")
	}
	return out, rest, nil
}
