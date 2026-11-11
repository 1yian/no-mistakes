package config

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/agentcfg"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

const (
	RoleReviewer           = "reviewer"
	RoleFixer              = "fixer"
	RoleReviewerAfterRound = "reviewer_after_round"
	RoleFixerAfterRound    = "fixer_after_round"
)

var ReviewAgentRoles = []string{RoleReviewer, RoleFixer, RoleReviewerAfterRound, RoleFixerAfterRound}

var baseRoleFor = map[string]string{
	RoleReviewerAfterRound: RoleReviewer,
	RoleFixerAfterRound:    RoleFixer,
}

type ReviewAgent struct {
	Pinned     bool            `yaml:"-"`
	Agent      types.AgentName `yaml:"agent"`
	Model      string          `yaml:"model"`
	Effort     agentcfg.Effort `yaml:"effort"`
	AfterRound *int            `yaml:"after_round"`
	Fallback   []ReviewAgent   `yaml:"fallback"`
}

func (e ReviewAgent) TakesOverFromRound() int {
	if e.AfterRound == nil {
		return 2
	}
	return *e.AfterRound + 1
}

func validateReviewAgents(roles map[string]ReviewAgent) error {
	for role, entry := range roles {
		if !slices.Contains(ReviewAgentRoles, role) {
			return fmt.Errorf("invalid review_agents role %q (valid: %s)", role, strings.Join(ReviewAgentRoles, ", "))
		}
		if !agentcfg.Known(entry.Agent) {
			return fmt.Errorf("review_agents.%s.agent must name an explicit harness, got %q", role, entry.Agent)
		}
		if err := agentcfg.Validate(entry.Agent, agentcfg.Profile{Model: strings.TrimSpace(entry.Model), Effort: entry.Effort}); err != nil {
			return fmt.Errorf("invalid review_agents.%s: %w", role, err)
		}
		if len(entry.Fallback) > 0 && role != RoleReviewer && role != RoleReviewerAfterRound {
			return fmt.Errorf("review_agents.%s does not accept fallback; only reviewer roles do", role)
		}
		for i, candidate := range entry.Fallback {
			if candidate.AfterRound != nil || len(candidate.Fallback) > 0 {
				return fmt.Errorf("review_agents.%s.fallback[%d] accepts only agent, model and effort", role, i)
			}
			if err := validateReviewAgents(map[string]ReviewAgent{RoleReviewer: candidate}); err != nil {
				return fmt.Errorf("review_agents.%s.fallback[%d]: %w", role, i, err)
			}
		}
		if _, isOverlay := baseRoleFor[role]; !isOverlay {
			if entry.AfterRound != nil {
				return fmt.Errorf("review_agents.%s does not accept after_round; configure review_agents.%s_after_round instead", role, role)
			}
			continue
		}
		if entry.AfterRound != nil {
			if *entry.AfterRound < 1 {
				return fmt.Errorf("review_agents.%s.after_round must be at least 1, got %d", role, *entry.AfterRound)
			}
			if *entry.AfterRound >= math.MaxInt {
				return fmt.Errorf("review_agents.%s.after_round overflows the takeover round", role)
			}
		}
	}
	return nil
}

func (c *Config) ReviewAgentTakeoverRound(role string) int {
	entry, ok := c.ReviewAgents[role]
	if !ok {
		return 0
	}
	return entry.TakesOverFromRound()
}

func (c *Config) ForReviewAgent(entry ReviewAgent) *Config {
	role := *c
	role.Agent = entry.Agent
	role.Agents = []types.AgentName{entry.Agent}
	profile := c.AgentProfileFor(entry.Agent)
	if entry.Model != "" {
		profile.Model = strings.TrimSpace(entry.Model)
	}
	if entry.Effort != "" {
		profile.Effort = entry.Effort
	}
	role.AgentConfig = map[string]agentcfg.Profile{string(entry.Agent): profile}
	role.ReviewAgents = nil
	if entry.Pinned {
		// A later global edit cannot displace a run's typed reviewer selection.
		role.AgentArgsOverride = maps.Clone(c.AgentArgsOverride)
		delete(role.AgentArgsOverride, string(entry.Agent))
		if entry.Agent == types.AgentPi {
			if role.AgentArgsOverride == nil {
				role.AgentArgsOverride = make(map[string][]string)
			}
			role.AgentArgsOverride[string(entry.Agent)] = (&agentcfg.PiProfile{Model: entry.Model, Effort: entry.Effort}).PinnedBaseArgs(nil)
		}
	}
	return &role
}
