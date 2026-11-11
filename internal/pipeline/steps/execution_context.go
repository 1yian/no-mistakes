package steps

import "github.com/kunchenguid/no-mistakes/internal/agent"

func executionContextPromptSection(workDir string) string {
	return agent.ExecutionContextPromptSection(workDir)
}
