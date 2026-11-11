package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/spf13/cobra"
)

const ciWorkflowGoSetup = `
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          check-latest: true
`

const ciWorkflowTemplateWithLint = `name: CI

on:
  push:
    branches: ['%s']
  pull_request:

permissions:
  contents: read

concurrency:
  group: ci-${{ github.ref }}
  cancel-in-progress: true

jobs:
  build-test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
%s
      - name: Lint
        run: |
          %s

      - name: Test
        run: |
          %s
`

const ciWorkflowTemplateTestOnly = `name: CI

on:
  push:
    branches: ['%s']
  pull_request:

permissions:
  contents: read

concurrency:
  group: ci-${{ github.ref }}
  cancel-in-progress: true

jobs:
  build-test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
%s
      - name: Test
        run: |
          %s
`

func newCIWorkflowCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "ci-workflow",
		Short: "Generate .github/workflows/ci.yml from .no-mistakes.yaml commands",
		Long: "Emits .github/workflows/ci.yml that mirrors the canonical checks pinned in .no-mistakes.yaml.\n" +
			"The workflow runs on push to the default branch and on all pull requests, registering\n" +
			"real GitHub checks that the no-mistakes gate's CI step can monitor.\n\n" +
			"Run this from inside a git repository that has been initialized with 'no-mistakes init'.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return trackCommand("ci-workflow", func() error {
				return generateCIWorkflow(".", force)
			})
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "overwrite existing workflow file")
	return cmd
}

func generateCIWorkflow(dir string, force bool) error {
	// Root the command at the git toplevel so the workflow always lands where
	// GitHub discovers it, regardless of the caller's working subdirectory.
	root, err := git.FindGitRoot(dir)
	if err != nil {
		return fmt.Errorf("ci-workflow: %w", err)
	}

	// Load the repo config
	cfg, err := config.LoadRepo(root)
	if err != nil {
		return fmt.Errorf("ci-workflow: %w", err)
	}

	// Resolve the repository's default branch
	// Use a timeout context for the remote lookup
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defaultBranch := git.DefaultBranch(ctx, root, "origin")

	// Extract test command (required)
	test := cfg.Commands.Test
	if test == "" {
		return fmt.Errorf("ci-workflow: commands.test not configured in .no-mistakes.yaml")
	}

	// Extract lint command (optional; empty means combined document+lint)
	lint := cfg.Commands.Lint

	var goSetup string
	if info, err := os.Stat(filepath.Join(root, "go.mod")); err == nil && !info.IsDir() {
		goSetup = ciWorkflowGoSetup
	}

	var workflow string
	if lint == "" {
		workflow = fmt.Sprintf(ciWorkflowTemplateTestOnly, yamlSingleQuote(defaultBranch), goSetup, indentCommand(test))
	} else {
		workflow = fmt.Sprintf(ciWorkflowTemplateWithLint, yamlSingleQuote(defaultBranch), goSetup, indentCommand(lint), indentCommand(test))
	}

	// Determine output path
	workflowDir := filepath.Join(root, ".github", "workflows")
	workflowPath := filepath.Join(workflowDir, "ci.yml")

	// Create .github/workflows, refusing to follow a symlink planted by a
	// repository-controlled tree component that would redirect the write
	// outside the repo root.
	if err := mkdirAllNoSymlink(root, workflowDir); err != nil {
		return fmt.Errorf("ci-workflow: %w", err)
	}

	// Write the workflow file, refusing a symlinked destination and
	// replacing any existing file atomically via temp+rename.
	if err := writeFileNoSymlink(workflowPath, []byte(workflow), force); err != nil {
		return fmt.Errorf("ci-workflow: %w", err)
	}

	fmt.Printf("  %s  %s\n", sGreen.Render("✓"), "CI workflow generated")
	fmt.Printf("  %s  %s\n", sDim.Render("    "), workflowPath)
	if lint == "" {
		fmt.Printf("  %s  %s\n", sDim.Render("    "), "(test only; lint uses combined document+lint)")
	}
	fmt.Println()
	fmt.Printf("  %s\n", sDim.Render("Commit and push this file to enable GitHub checks:"))
	fmt.Printf("  %s\n", sBold.Render("git add .github/workflows/ci.yml && git commit -m 'ci: register workflow'"))

	return nil
}

const ciWorkflowRunIndent = "          "

func indentCommand(cmd string) string {
	lines := strings.Split(cmd, "\n")
	for i := 1; i < len(lines); i++ {
		lines[i] = ciWorkflowRunIndent + lines[i]
	}
	return strings.Join(lines, "\n")
}

func yamlSingleQuote(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

func mkdirAllNoSymlink(root, target string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return fmt.Errorf("resolve %s relative to %s: %w", target, root, err)
	}
	current := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if !os.IsNotExist(err) {
				return fmt.Errorf("stat %s: %w", current, err)
			}
			if err := os.Mkdir(current, 0755); err != nil {
				return fmt.Errorf("create %s: %w", current, err)
			}
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to follow symlink at %s", current)
		}
		if !info.IsDir() {
			return fmt.Errorf("%s exists and is not a directory", current)
		}
	}
	return nil
}

func writeFileNoSymlink(path string, data []byte, force bool) error {
	info, err := os.Lstat(path)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to write through symlink %s", path)
		}
		if !force {
			return fmt.Errorf("%s already exists (use --force to overwrite)", path)
		}
	case !os.IsNotExist(err):
		return fmt.Errorf("stat %s: %w", path, err)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".ci-workflow-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Chmod(0644); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temp file into place: %w", err)
	}
	return nil
}
