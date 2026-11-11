package agent

import (
	"context"
	"fmt"
)

func WorktreeSteering(evidenceRoot string) string {
	location := evidenceRoot
	if location == "" {
		location = "the run's managed evidence directory"
	}
	return fmt.Sprintf(`Workspace boundary (important):
- Confine source, project, user-data, and system file changes to the current working directory, which is a git worktree. Do not intentionally create, modify, move, or delete those files anywhere outside it.
- Do not modify system state outside the worktree. In particular, do not install or upgrade system packages (for example brew install/upgrade, or other system package managers), do not modify applications under /Applications, and do not change global or user-level tool configuration.
- This is prompt steering, not true enforcement: treat the worktree boundary as a soft boundary you must follow.
- The only allowed out-of-worktree writes are test evidence files under %s when a testing prompt explicitly asks for them.
- Ephemeral temp/cache writes that are incidental side effects of running the project development toolchain are allowed outside the worktree for tests, linters, formatters, builds, and manual verification commands.
- You may read files outside the worktree and run read-only commands, but every other intentional write must stay inside the worktree.
- Do not search the host filesystem. Never run a filesystem-wide search such as `+"`find /`"+` or `+"`mdfind /`"+`, and never hunt the machine for an installed tool. A `+"`-maxdepth`"+` or `+"`-xdev`"+` flag does not make a whole-root search bounded, so it does not license one. Bounded searches inside the worktree, inside an external evidence path a prompt explicitly names, or inside a repository-local path you were given remain fine.
- If a tool you need is not on PATH and no repository-local path is supplied, do not search the machine for it: report the missing tool and the work it blocked in your normal result, with the concrete reason, and stop searching there. A missing tool blocks only the search and any verification that needs it: still do every other part of your task, including any edits you were asked to make.
- Only add new commits on top of the current HEAD. Never amend, squash, reword, reorder, drop, or reset an existing commit, and never otherwise rewrite history, unless your prompt explicitly asks you to run or continue a rebase or merge. The pipeline records the head it validated, and a rewritten history strands the run. This holds even when a finding or instruction asks you to change a commit message or an existing commit: make any file changes it needs as new work, and report that you left history unchanged.

`, location)
}

type steeredAgent struct {
	Agent
	preamble string
}

func (s steeredAgent) Run(ctx context.Context, opts RunOpts) (*Result, error) {
	opts.Prompt = s.preamble + opts.Prompt
	return s.Agent.Run(ctx, opts)
}

func (s steeredAgent) SupportsSessionResume() bool {
	return SupportsSessionResume(s.Agent)
}

func (s steeredAgent) SupportsSessionProvider(provider string) bool {
	return SupportsSessionProvider(s.Agent, provider)
}

func (s steeredAgent) ReportsAgentAttempts() bool {
	return ReportsAgentAttempts(s.Agent)
}

func (s steeredAgent) NeutralizesGateInstructions() bool {
	return NeutralizesGateInstructions(s.Agent)
}

func WithSteering(a Agent, evidenceRoot string) Agent {
	if a == nil {
		return nil
	}
	if _, ok := a.(steeredAgent); ok {
		return a
	}
	return steeredAgent{Agent: a, preamble: WorktreeSteering(evidenceRoot)}
}
