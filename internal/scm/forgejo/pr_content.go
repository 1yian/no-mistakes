package forgejo

import (
	"context"
	"fmt"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/scm"
)

func (h *Host) GetPRContent(ctx context.Context, pr *scm.PR) (scm.PRContent, error) {
	number, err := h.validateInputPR(pr)
	if err != nil {
		return scm.PRContent{}, err
	}
	var response struct {
		Status int `json:"status"`
		Data   *struct {
			Number int     `json:"number"`
			Title  string  `json:"title"`
			Body   *string `json:"body"`
		} `json:"data"`
	}
	endpoint := "repos/" + h.repository + "/pulls/" + number
	if err := h.runJSON(ctx, "api", []string{"GET", endpoint}, &response); err != nil {
		return scm.PRContent{}, err
	}
	if response.Status != 200 || response.Data == nil || strings.TrimSpace(response.Data.Title) == "" || response.Data.Body == nil {
		return scm.PRContent{}, fmt.Errorf("forgejo-axi api: incomplete raw pull content (requires raw api support)")
	}
	if err := h.validateOutputPRNumber(number, response.Data.Number); err != nil {
		return scm.PRContent{}, err
	}
	return scm.PRContent{Title: response.Data.Title, Body: *response.Data.Body}, nil
}

var _ scm.PRContentReader = (*Host)(nil)
