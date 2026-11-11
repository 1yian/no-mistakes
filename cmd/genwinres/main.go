package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/kunchenguid/no-mistakes/internal/winres"
)

func main() {
	os.Exit(run(os.Args[1:], ".", os.Stdout, os.Stderr))
}

func run(args []string, root string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("genwinres", flag.ContinueOnError)
	flags.SetOutput(stderr)
	check := flags.Bool("check", false, "verify the committed resource objects match the generator instead of writing them")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	stale := false
	for _, target := range winres.Targets {
		rel := filepath.FromSlash(target.ObjectPath())
		path := filepath.Join(root, rel)
		want := target.Object()

		if *check {
			got, err := os.ReadFile(path)
			if err != nil {
				fmt.Fprintf(stderr, "genwinres --check: read %s: %v\n", rel, err)
				stale = true
				continue
			}
			if !bytes.Equal(got, want) {
				fmt.Fprintf(stderr, "genwinres --check: %s is stale; run `go run ./cmd/genwinres` and commit the result\n", rel)
				stale = true
				continue
			}
			fmt.Fprintf(stdout, "genwinres: %s is up to date\n", rel)
			continue
		}

		if err := os.WriteFile(path, want, 0o644); err != nil {
			fmt.Fprintf(stderr, "genwinres: write %s: %v\n", rel, err)
			return 1
		}
		fmt.Fprintf(stdout, "genwinres: wrote %s\n", rel)
	}
	if stale {
		return 1
	}
	return 0
}
