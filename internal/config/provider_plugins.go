package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/scm"
)

const DefaultProviderPluginTimeout = 2 * time.Minute

var providerPluginNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

type ProviderPlugin struct {
	Command           string
	Args              []string
	Hosts             []string
	Timeout           time.Duration
	DraftPullRequests bool
}

type ProviderPlugins map[string]ProviderPlugin

type providerPluginRaw struct {
	Command           string   `yaml:"command"`
	Args              []string `yaml:"args"`
	Hosts             []string `yaml:"hosts"`
	Timeout           string   `yaml:"timeout"`
	DraftPullRequests bool     `yaml:"draft_pull_requests"`
}

func normalizeProviderPlugins(raw map[string]providerPluginRaw) (ProviderPlugins, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	plugins := make(ProviderPlugins, len(raw))
	owners := map[string]string{}
	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entry := raw[name]
		if !providerPluginNamePattern.MatchString(name) {
			return nil, fmt.Errorf("invalid provider_plugins.%s: name must be 1-63 lowercase letters, digits, '-' or '_', starting with a letter or digit", name)
		}
		for _, builtin := range scm.BuiltinProviders {
			if name == string(builtin) {
				return nil, fmt.Errorf("invalid provider_plugins.%s: %q is a built-in provider name", name, name)
			}
		}
		command, err := normalizeProviderPluginCommand(entry.Command)
		if err != nil {
			return nil, fmt.Errorf("invalid provider_plugins.%s.command: %w", name, err)
		}
		if len(entry.Hosts) == 0 {
			return nil, fmt.Errorf("invalid provider_plugins.%s.hosts: at least one host is required", name)
		}
		hosts := make([]string, 0, len(entry.Hosts))
		for _, rawHost := range entry.Hosts {
			host, err := normalizeProviderPluginHost(rawHost)
			if err != nil {
				return nil, fmt.Errorf("invalid provider_plugins.%s.hosts: %w", name, err)
			}
			if owner, taken := owners[host]; taken {
				return nil, fmt.Errorf("invalid provider_plugins.%s.hosts: %q is already claimed by provider_plugins.%s", name, host, owner)
			}
			owners[host] = name
			hosts = append(hosts, host)
		}
		timeout := DefaultProviderPluginTimeout
		if strings.TrimSpace(entry.Timeout) != "" {
			timeout, err = parsePositiveDuration("provider_plugins."+name+".timeout", strings.TrimSpace(entry.Timeout))
			if err != nil {
				return nil, err
			}
		}
		plugins[name] = ProviderPlugin{
			Command:           command,
			Args:              append([]string(nil), entry.Args...),
			Hosts:             hosts,
			Timeout:           timeout,
			DraftPullRequests: entry.DraftPullRequests,
		}
	}
	return plugins, nil
}

func normalizeProviderPluginCommand(raw string) (string, error) {
	command := strings.TrimSpace(raw)
	if command == "" {
		return "", fmt.Errorf("command is required")
	}
	if !strings.ContainsAny(command, `/\`) {
		return command, nil
	}
	return normalizeForgeProfilePath(command)
}

func normalizeProviderPluginHost(raw string) (string, error) {
	host := strings.ToLower(strings.TrimSpace(raw))
	if host == "" {
		return "", fmt.Errorf("host must not be empty")
	}
	suffix := strings.TrimPrefix(host, "*.")
	if suffix == "" || strings.ContainsAny(suffix, "*/:@ \t") || strings.HasPrefix(suffix, ".") || strings.HasSuffix(suffix, ".") || strings.Contains(suffix, "..") {
		return "", fmt.Errorf("%q is not a host name or *.<domain> pattern (no scheme, port, path, or user)", raw)
	}
	return host, nil
}

func (p ProviderPlugins) Select(rawHost string, resolvedHost func() string) (string, bool) {
	if len(p) == 0 {
		return "", false
	}
	if name, ok := p.selectHost(rawHost); ok {
		return name, true
	}
	if resolvedHost == nil {
		return "", false
	}
	if resolved := resolvedHost(); resolved != "" && !strings.EqualFold(resolved, rawHost) {
		return p.selectHost(resolved)
	}
	return "", false
}

func (p ProviderPlugins) selectHost(host string) (string, bool) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return "", false
	}
	best, bestLen, exact := "", -1, false
	for name, plugin := range p {
		for _, pattern := range plugin.Hosts {
			switch {
			case pattern == host:
				if !exact {
					best, bestLen, exact = name, len(pattern), true
				}
			case !exact && strings.HasPrefix(pattern, "*.") && strings.HasSuffix(host, pattern[1:]) && len(pattern) > bestLen:
				best, bestLen = name, len(pattern)
			}
		}
	}
	return best, bestLen >= 0
}

func validateProviderPluginsAgainstForgeProfiles(plugins ProviderPlugins, profiles ForgeProfiles) error {
	if len(plugins) == 0 || len(profiles) == 0 {
		return nil
	}
	hosts := make([]string, 0, len(profiles))
	for host := range profiles {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	for _, host := range hosts {
		hostToken, _ := SplitForgeProfileKey(host)
		if name, ok := plugins.selectHost(hostToken); ok {
			return fmt.Errorf("invalid forge_profiles.%s: host is also claimed by provider_plugins.%s", host, name)
		}
	}
	return nil
}
