package intent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/runenv"
)

type Result struct {
	Summary   string
	AgentName string
	SessionID string
	Score     float64
}

type ExtractParams struct {
	TranscriptHomes *runenv.TranscriptHomes
	HomeDir         string
	OriginCWD       string
	DiffFiles       []string
	Change          ChangeContext
	BaseTime        time.Time
	HeadTime        time.Time
	SlackDays       int
	Threshold       float64
	Readers         []Reader
	Cache           Cache
	Summarizer      Summarizer
	Disambiguator   Disambiguator
	Logf            func(format string, args ...any)
}

var ErrNoMatch = errors.New("intent: no matching transcript")

func Extract(ctx context.Context, p ExtractParams) (*Result, error) {
	if p.OriginCWD == "" {
		return nil, fmt.Errorf("intent: OriginCWD is required")
	}
	if len(p.DiffFiles) == 0 {
		return nil, ErrNoMatch
	}
	if p.Cache == nil {
		p.Cache = NewMemCache()
	}
	if p.Summarizer == nil {
		return nil, fmt.Errorf("intent: Summarizer is required")
	}

	slack := time.Duration(maxInt(p.SlackDays, 0)) * 24 * time.Hour
	opts := DiscoverOpts{
		TranscriptHomes: p.TranscriptHomes,
		HomeDir:         p.HomeDir,
		OriginCWD:       canonicalPath(p.OriginCWD),
		WindowStart:     p.BaseTime.Add(-slack),
		WindowEnd:       p.HeadTime,
	}

	var sessions []*Session
	for _, r := range p.Readers {
		if r == nil {
			continue
		}
		discovered, err := r.Discover(ctx, opts)
		if err != nil {
			slog.Debug("intent reader discover failed", "agent", r.Name(), "error", err)
			if p.Logf != nil {
				p.Logf("%s discovery disabled: %v", r.Name(), err)
			}
			continue
		}
		for _, s := range discovered {
			s.AgentName = r.Name()
		}
		sessions = append(sessions, discovered...)
	}

	if len(sessions) == 0 {
		return nil, ErrNoMatch
	}

	var loaded []*Session
	for _, s := range sessions {
		var reader Reader
		for _, r := range p.Readers {
			if r != nil && r.Name() == s.AgentName {
				reader = r
				break
			}
		}
		if reader == nil {
			continue
		}
		if err := reader.Load(ctx, s); err != nil {
			slog.Debug("intent reader load failed", "agent", s.AgentName, "session", s.SessionID, "error", err)
			continue
		}
		loaded = append(loaded, s)
	}

	match := pickMatchWithOptions(loaded, p.DiffFiles, matchOptions{
		Threshold: p.Threshold,
		HeadTime:  p.HeadTime,
		Logf:      p.Logf,
	})
	if match == nil {
		return nil, ErrNoMatch
	}
	match, err := disambiguateMatch(ctx, p, match, loaded)
	if err != nil {
		return nil, err
	}

	change := SummaryContext{Change: p.Change, WindowStart: opts.WindowStart, WindowEnd: opts.WindowEnd}
	key := summaryCacheKeyFor(match.Session, change)
	if cached, ok := p.Cache.Get(key); ok && cached != "" {
		return &Result{
			Summary:   cached,
			AgentName: match.Session.AgentName,
			SessionID: match.Session.SessionID,
			Score:     match.Score,
		}, nil
	}

	summary, err := p.Summarizer.Summarize(ctx, match.Session, change)
	if err != nil {
		return nil, fmt.Errorf("intent: summarize: %w", err)
	}
	p.Cache.Put(key, summary, match.Session.AgentName, match.Session.SessionID)

	return &Result{
		Summary:   summary,
		AgentName: match.Session.AgentName,
		SessionID: match.Session.SessionID,
		Score:     match.Score,
	}, nil
}

func disambiguateMatch(ctx context.Context, p ExtractParams, fallback *Match, loaded []*Session) (*Match, error) {
	if p.Disambiguator == nil {
		return fallback, nil
	}
	candidates := acceptedMatches(loaded, p.DiffFiles, matchOptions{
		Threshold: p.Threshold,
		HeadTime:  p.HeadTime,
	})
	if !shouldDisambiguate(candidates) {
		return fallback, nil
	}
	choice, err := p.Disambiguator.Disambiguate(ctx, p.DiffFiles, candidates)
	if err != nil {
		if errors.Is(err, ErrDisambiguatorCleanup) {
			return nil, fmt.Errorf("intent: disambiguator cleanup: %w", err)
		}
		if p.Logf != nil {
			p.Logf("disambiguator failed: %v", err)
		}
		return fallback, nil
	}
	for _, candidate := range candidates {
		if candidate.Session != nil && candidate.Session.AgentName == choice.AgentName && candidate.Session.SessionID == choice.SessionID {
			return candidate, nil
		}
	}
	if p.Logf != nil {
		p.Logf("disambiguator returned unknown session %q/%q", choice.AgentName, choice.SessionID)
	}
	return fallback, nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
