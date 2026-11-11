package config

import (
	"fmt"
	"maps"
	"slices"

	"github.com/kunchenguid/no-mistakes/internal/agentcfg"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func (c *GlobalConfig) ResolvePiProfile(request *agentcfg.PiProfile) (*agentcfg.PiProfile, error) {
	if request == nil {
		return nil, nil
	}
	if err := request.ValidateRequest(); err != nil {
		return nil, err
	}
	if slices.Contains(c.AgentArgsOverride["pi"], "--") {
		return nil, fmt.Errorf("Pi run profile conflicts with '--' in agent_args_override.pi")
	}
	if _, found := agentcfg.PiSelectionArgs(c.AgentArgsOverride["pi"]); found {
		return nil, fmt.Errorf("Pi run profile conflicts with native selection flags in agent_args_override.pi; use agent_config.pi defaults instead")
	}
	defaults := c.AgentConfig["pi"]
	resolved := *request
	if resolved.Model == "" {
		resolved.Model = defaults.Model
	}
	if resolved.Effort == "" {
		resolved.Effort = defaults.Effort
	}
	if err := resolved.Validate(); err != nil {
		return nil, err
	}
	return &resolved, nil
}

func (c *Config) ValidatePiProfileAgents() error {
	return validatePiProfileAgents(c.Agent, c.Agents, c.ReviewAgents)
}

func validatePiProfileAgents(agent types.AgentName, agents []types.AgentName, reviewAgents map[string]ReviewAgent) error {
	names := agents
	if len(names) == 0 {
		names = []types.AgentName{agent}
	}
	for _, name := range names {
		if name != types.AgentPi {
			return fmt.Errorf("Pi run profile requires agent: pi without non-Pi fallbacks")
		}
	}
	for _, entry := range reviewAgents {
		if entry.Agent != types.AgentPi {
			return fmt.Errorf("Pi run profile conflicts with non-Pi review_agents")
		}
		for _, fallback := range entry.Fallback {
			if fallback.Agent != types.AgentPi {
				return fmt.Errorf("Pi run profile conflicts with non-Pi review_agents fallback")
			}
		}
	}
	return nil
}

func (c *Config) ApplyPiProfile(pin *agentcfg.PiProfile) error {
	if pin == nil {
		return nil
	}
	if err := pin.Validate(); err != nil {
		return err
	}
	if slices.Contains(c.AgentArgsFor(types.AgentPi), "--") {
		return fmt.Errorf("cannot enforce Pi run pin after '--' in agent_args_override.pi")
	}
	c.Agent = types.AgentPi
	c.Agents = []types.AgentName{types.AgentPi}
	c.ReviewAgents = nil // both roles now use the same pinned primary
	c.AgentConfig = map[string]agentcfg.Profile{"pi": {Model: pin.Model, Effort: pin.Effort}}
	args := pin.PinnedBaseArgs(c.AgentArgsFor(types.AgentPi))
	c.AgentArgsOverride = maps.Clone(c.AgentArgsOverride)
	if c.AgentArgsOverride == nil {
		c.AgentArgsOverride = make(map[string][]string)
	}
	c.AgentArgsOverride["pi"] = args
	return nil
}
