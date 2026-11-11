package gitea

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/scm"
)

func (h *Host) GetPRContent(ctx context.Context, pr *scm.PR) (scm.PRContent, error) {
	id, err := giteaPRNumber(pr)
	if err != nil {
		return scm.PRContent{}, err
	}
	number, err := strconv.Atoi(id)
	if err != nil || number <= 0 {
		return scm.PRContent{}, fmt.Errorf("invalid Gitea pull number")
	}
	owner, repo, ok := splitOwnerRepo(h.repoSlug)
	if !ok {
		return scm.PRContent{}, fmt.Errorf("invalid Gitea repository")
	}
	_, account, err := h.begin(ctx)
	if err != nil {
		return scm.PRContent{}, err
	}
	endpoint := fmt.Sprintf("/repos/%s/%s/pulls/%s", owner, repo, id)
	args := append([]string{"api"}, account...)
	out, err := h.cmd(ctx, "tea", append(args, endpoint)...).Output()
	if err != nil {
		return scm.PRContent{}, fmt.Errorf("tea api pull content: %w", err)
	}
	var raw struct {
		Number int     `json:"number"`
		Title  string  `json:"title"`
		Body   *string `json:"body"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return scm.PRContent{}, fmt.Errorf("parse tea raw pull: %w", err)
	}
	if raw.Number != number || strings.TrimSpace(raw.Title) == "" || raw.Body == nil {
		return scm.PRContent{}, fmt.Errorf("tea api pull: incomplete or mismatched raw content")
	}
	return scm.PRContent{Title: raw.Title, Body: *raw.Body}, nil
}

var _ scm.PRContentReader = (*Host)(nil)
