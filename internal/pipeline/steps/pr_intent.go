package steps

import (
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/pipeline"
)

func publicPRIntent(sctx *pipeline.StepContext) string {
	source := publicPRIntentSource(sctx)
	if source == "" {
		return ""
	}
	if sctx.Config != nil && strings.EqualFold(strings.TrimSpace(sctx.Config.PR.Intent.Format), "brief") {
		return strings.TrimSpace(sctx.PublicIntentBrief)
	}
	return source
}

func publicPRIntentSource(sctx *pipeline.StepContext) string {
	if sctx != nil && sctx.Config != nil && !sctx.Config.PR.PublishesIntent() {
		return ""
	}
	if runOmitsIntent(sctx) {
		return ""
	}
	return cleanedUserIntent(sctx)
}

func runOmitsIntent(sctx *pipeline.StepContext) bool {
	return sctx != nil && sctx.Run != nil && sctx.Run.OmitIntent
}

func prDraftIntentPromptSection(sctx *pipeline.StepContext) string {
	if runOmitsIntent(sctx) {
		return ""
	}
	return userIntentPromptSection(sctx)
}
