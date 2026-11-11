package scm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/shellenv"
	"github.com/kunchenguid/no-mistakes/internal/winproc"
	"gopkg.in/yaml.v3"
)

type Provider string

const (
	ProviderGitHub      Provider = "github"
	ProviderGitLab      Provider = "gitlab"
	ProviderBitbucket   Provider = "bitbucket"
	ProviderAzureDevOps Provider = "azuredevops"
	ProviderForgejo     Provider = "forgejo"
	ProviderGitea       Provider = "gitea"
	ProviderOrigin      Provider = "origin"
	ProviderUnknown     Provider = "unknown"
)

const pluginProviderPrefix = "plugin:"

var BuiltinProviders = []Provider{
	ProviderGitHub,
	ProviderGitLab,
	ProviderBitbucket,
	ProviderAzureDevOps,
	ProviderForgejo,
	ProviderGitea,
	ProviderOrigin,
	ProviderUnknown,
}

func PluginProvider(name string) Provider {
	return Provider(pluginProviderPrefix + name)
}

func (p Provider) PluginName() (string, bool) {
	name, ok := strings.CutPrefix(string(p), pluginProviderPrefix)
	if !ok || name == "" {
		return "", false
	}
	return name, true
}

func (p Provider) IsPlugin() bool {
	_, ok := p.PluginName()
	return ok
}

type sshHostnameLookup func(context.Context, string) (string, error)

func DetectProvider(remoteURL string) Provider {
	return DetectProviderContext(context.Background(), remoteURL)
}

func DetectProviderContext(ctx context.Context, remoteURL string) Provider {
	return detectProvider(ctx, remoteURL, lookupSSHHostname)
}

func DetectProviderWithForgejoBaseURL(remoteURL, forgejoBaseURL string) Provider {
	return DetectProviderContextWithForgejoBaseURL(context.Background(), remoteURL, forgejoBaseURL)
}

type ForgejoEnvironment struct {
	BaseURL   string
	SSHDomain string
}

func DetectProviderContextWithForgejo(ctx context.Context, remoteURL string, forgejo ForgejoEnvironment) Provider {
	return detectProviderWithForgejo(ctx, remoteURL, forgejo, lookupSSHHostname)
}

func DetectProviderStaticContext(ctx context.Context, url string) Provider {
	if provider := detectStaticProvider(url); provider != ProviderUnknown {
		return provider
	}
	host := resolveHost(ctx, url, lookupSSHHostname)
	if host == "" || strings.EqualFold(host, ExtractHost(url)) {
		return ProviderUnknown
	}
	return detectStaticProvider(host)
}

func detectStaticProvider(url string) Provider {
	lower := strings.ToLower(url)
	if strings.Contains(lower, "codeberg.org") || strings.Contains(lower, "forgejo") {
		return ProviderForgejo
	}
	if detectCursorOrigin(url) {
		return ProviderOrigin
	}
	return detectLegacyProviderHost(lower)
}

func DetectProviderContextWithForgejoBaseURL(ctx context.Context, remoteURL, forgejoBaseURL string) Provider {
	return detectProviderWithForgejoBaseURL(ctx, remoteURL, forgejoBaseURL, lookupSSHHostname)
}

func detectProvider(ctx context.Context, remoteURL string, lookup sshHostnameLookup) Provider {
	return detectProviderWithForgejo(ctx, remoteURL, ForgejoEnvironment{
		BaseURL:   os.Getenv("FORGEJO_BASE_URL"),
		SSHDomain: os.Getenv("FORGEJO_SSH_DOMAIN"),
	}, lookup)
}

func detectProviderWithForgejoBaseURL(ctx context.Context, remoteURL, forgejoBaseURL string, lookup sshHostnameLookup) Provider {
	return detectProviderWithForgejo(ctx, remoteURL, ForgejoEnvironment{BaseURL: forgejoBaseURL}, lookup)
}

func detectProviderWithForgejo(ctx context.Context, remoteURL string, forgejo ForgejoEnvironment, lookup sshHostnameLookup) Provider {
	originalHost := ExtractHost(remoteURL)
	host := resolveHost(ctx, remoteURL, lookup)
	if host == "" {
		return ProviderUnknown
	}
	if provider := detectHostedProvider(host); provider != ProviderUnknown {
		return provider
	}
	if detectCursorOriginWeb(remoteURL) {
		return ProviderOrigin
	}
	if glabKnowsHost(host) {
		return ProviderGitLab
	}
	if ghKnowsHost(host) {
		return ProviderGitHub
	}
	// Fallback for Gitea, which is nearly always self-hosted at an arbitrary
	// hostname with no distinguishing substring at all: consult the tea CLI's
	// own login config. If the remote's host is one tea is configured to talk
	// to, treat it as Gitea.
	if teaKnowsHost(host) {
		return ProviderGitea
	}
	if strings.EqualFold(host, originalHost) {
		if forgejoBaseMatchesRemote(forgejo.BaseURL, remoteURL) {
			return ProviderForgejo
		}
	} else if forgejoBaseMatchesResolvedRemote(forgejo.BaseURL, remoteURL, host) {
		return ProviderForgejo
	}
	// A declared SSH domain deliberately has the same standing as the base-URL
	// matches above: consulted after the hosted providers and the glab, gh, and
	// tea configs, and ahead of the substring host heuristic below.
	if forgejoSSHDomainMatchesRemote(forgejo, remoteURL, host) {
		return ProviderForgejo
	}
	if host == "codeberg.org" || strings.Contains(host, "forgejo") {
		return ProviderForgejo
	}
	return detectLegacyProviderHost(host)
}

func detectHostedProvider(host string) Provider {
	switch {
	case host == "github.com" || strings.HasSuffix(host, ".github.com"):
		return ProviderGitHub
	case host == "gitlab.com" || strings.HasSuffix(host, ".gitlab.com"):
		return ProviderGitLab
	case host == "bitbucket.org" || strings.HasSuffix(host, ".bitbucket.org"):
		return ProviderBitbucket
	case host == "dev.azure.com" || strings.HasSuffix(host, ".dev.azure.com") || strings.HasSuffix(host, ".visualstudio.com"):
		return ProviderAzureDevOps
	case isCursorOriginGitHost(host):
		return ProviderOrigin
	default:
		return ProviderUnknown
	}
}

func detectLegacyProviderHost(host string) Provider {
	switch {
	case strings.Contains(host, "github.com"):
		return ProviderGitHub
	case strings.Contains(host, "gitlab.com") || strings.Contains(host, "gitlab."):
		return ProviderGitLab
	case strings.Contains(host, "bitbucket.org"):
		return ProviderBitbucket
	case strings.Contains(host, "dev.azure.com") || strings.Contains(host, "visualstudio.com"):
		// Covers dev.azure.com, ssh.dev.azure.com, {org}.visualstudio.com, and
		// the legacy vs-ssh.visualstudio.com SSH host.
		return ProviderAzureDevOps
	default:
		return ProviderUnknown
	}
}

func isCursorOriginGitHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	return host == "origin.cursor.com" || strings.HasSuffix(host, ".origin.cursor.com")
}

func detectCursorOrigin(remote string) bool {
	if isCursorOriginGitHost(ExtractHost(remote)) {
		return true
	}
	return detectCursorOriginWeb(remote)
}

func detectCursorOriginWeb(remote string) bool {
	if ExtractHost(remote) != "cursor.com" {
		return false
	}
	path := strings.ToLower(RepoPath(remote))
	return strings.HasPrefix(path, "codebase/")
}

func ResolveHost(ctx context.Context, remote string) string {
	return resolveHost(ctx, remote, lookupSSHHostname)
}

func resolveHost(ctx context.Context, remote string, lookup sshHostnameLookup) string {
	host := ExtractHost(remote)
	if host == "" || !isSSHRemote(remote) || lookup == nil {
		return host
	}

	resolved, err := lookup(ctx, host)
	if err != nil {
		return host
	}
	resolved = strings.ToLower(strings.TrimSpace(stripPort(resolved)))
	if resolved == "" {
		return host
	}
	return canonicalProviderHost(resolved)
}

func canonicalProviderHost(host string) string {
	switch host {
	case "ssh.github.com":
		return "github.com"
	case "altssh.gitlab.com":
		return "gitlab.com"
	default:
		return host
	}
}

func isSSHRemote(remote string) bool {
	remote = strings.TrimSpace(remote)
	lower := strings.ToLower(remote)
	if strings.HasPrefix(lower, "ssh://") {
		return true
	}
	if strings.Contains(remote, "://") {
		return false
	}
	colon := strings.IndexByte(remote, ':')
	if colon <= 0 {
		return false
	}
	if colon == 1 && len(remote) >= 3 && ((remote[0] >= 'a' && remote[0] <= 'z') || (remote[0] >= 'A' && remote[0] <= 'Z')) && (remote[2] == '/' || remote[2] == '\\') {
		return false
	}
	slash := strings.IndexAny(remote, `/\\`)
	return slash < 0 || colon < slash
}

func lookupSSHHostname(ctx context.Context, alias string) (string, error) {
	lookupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(lookupCtx, "ssh", "-G", "--", alias)
	shellenv.ConfigureShellCommand(cmd)
	out, err := shellenv.OutputShellCommand(cmd)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.EqualFold(fields[0], "hostname") {
			return fields[1], nil
		}
	}
	return "", nil
}

func forgejoBaseMatchesRemote(baseRaw, remoteRaw string) bool {
	_, base, err := NormalizeForgejoBaseURL(baseRaw)
	if err != nil {
		return false
	}
	remoteHost, remotePath, remoteScheme, ok := providerRemoteParts(remoteRaw)
	if !ok {
		return false
	}
	if remoteScheme == "http" || remoteScheme == "https" {
		if normalizedHTTPAuthority(remoteScheme, remoteHost) != normalizedHTTPAuthority(base.Scheme, base.Host) {
			return false
		}
	} else if !strings.EqualFold(stripPort(remoteHost), base.Hostname()) {
		// SSH clone ports commonly differ from the Forgejo web port.
		return false
	}
	return forgejoPathsMatch(base.Path, remotePath)
}

func normalizedHTTPAuthority(scheme, authority string) string {
	parsed := &url.URL{Host: authority}
	hostname := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if (strings.EqualFold(scheme, "http") && port == "80") || (strings.EqualFold(scheme, "https") && port == "443") {
		port = ""
	}
	if port != "" {
		return net.JoinHostPort(hostname, port)
	}
	if strings.Contains(hostname, ":") {
		return "[" + hostname + "]"
	}
	return hostname
}

func forgejoBaseMatchesResolvedRemote(baseRaw, remoteRaw, resolvedHost string) bool {
	_, base, err := NormalizeForgejoBaseURL(baseRaw)
	if err != nil {
		return false
	}
	if !strings.EqualFold(stripPort(resolvedHost), base.Hostname()) {
		return false
	}
	_, remotePath, _, ok := providerRemoteParts(remoteRaw)
	return ok && forgejoPathsMatch(base.Path, remotePath)
}

func forgejoSSHDomainMatchesRemote(forgejo ForgejoEnvironment, remoteRaw, resolvedHost string) bool {
	domain, err := NormalizeForgejoSSHDomain(forgejo.SSHDomain)
	if err != nil {
		return false
	}
	_, base, err := NormalizeForgejoBaseURL(forgejo.BaseURL)
	if err != nil {
		return false
	}
	if !ForgejoSSHDomainMatchesHost(resolvedHost, domain) {
		return false
	}
	_, remotePath, remoteScheme, ok := providerRemoteParts(remoteRaw)
	if !ok || remoteScheme != "ssh" {
		return false
	}
	return forgejoPathsMatch(base.Path, remotePath)
}

func forgejoPathsMatch(basePath, remotePath string) bool {
	prefix := providerPathParts(basePath)
	remote := providerPathParts(remotePath)
	if len(remote) == len(prefix)+4 && remote[len(remote)-2] == "pulls" {
		number, err := strconv.Atoi(remote[len(remote)-1])
		if err != nil || number <= 0 {
			return false
		}
		remote = remote[:len(remote)-2]
	}
	if len(remote) != len(prefix)+2 {
		return false
	}
	for i := range prefix {
		if remote[i] != prefix[i] {
			return false
		}
	}
	return true
}

func providerRemoteParts(raw string) (host, remotePath, scheme string, ok bool) {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, "://") {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return "", "", "", false
		}
		scheme = strings.ToLower(parsed.Scheme)
		if !ForgejoRemoteSchemeSupported(scheme) {
			return "", "", "", false
		}
		return parsed.Host, parsed.Path, scheme, true
	}
	colon := strings.Index(raw, ":")
	if colon <= 0 {
		return "", "", "", false
	}
	host = raw[:colon]
	if at := strings.LastIndex(host, "@"); at >= 0 {
		host = host[at+1:]
	}
	return host, raw[colon+1:], "ssh", host != ""
}

func providerPathParts(raw string) []string {
	raw = strings.Trim(strings.TrimSpace(raw), "/")
	raw = strings.TrimSuffix(raw, ".git")
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "/")
}

func NormalizeForgejoBaseURL(raw string) (string, *url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", nil, fmt.Errorf("invalid Forgejo base URL %q", raw)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	hostname := strings.ToLower(parsed.Hostname())
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || hostname == "" {
		return "", nil, fmt.Errorf("invalid Forgejo base URL %q", raw)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", nil, errors.New("Forgejo base URL must not contain credentials, a query, or a fragment")
	}
	port := parsed.Port()
	if (parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		parsed.Host = net.JoinHostPort(hostname, port)
	} else if strings.Contains(hostname, ":") {
		parsed.Host = "[" + hostname + "]"
	} else {
		parsed.Host = hostname
	}
	parsed.Path = strings.TrimRight(path.Clean("/"+strings.Trim(parsed.Path, "/")), "/")
	if parsed.Path == "." || parsed.Path == "/" {
		parsed.Path = ""
	}
	parsed.RawPath = ""
	return strings.TrimRight(parsed.String(), "/"), parsed, nil
}

var errInvalidForgejoSSHDomain = errors.New("invalid Forgejo SSH domain; expected a bare hostname or IP address, with an optional port")

func NormalizeForgejoSSHDomain(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	// The character filter rejects every shape a bare hostname cannot have: a
	// scheme or path (/), credentials (@), a query (?), and a fragment (#).
	if value == "" || strings.ContainsAny(value, "/@?#\\") {
		return "", errInvalidForgejoSSHDomain
	}
	// Parsing through a synthetic authority leaves host and port syntax,
	// including a bracketed IPv6 literal, to net/url instead of hand-rolled
	// splitting.
	parsed, err := url.Parse("ssh://" + value)
	if err != nil {
		return "", errInvalidForgejoSSHDomain
	}
	hostname := strings.ToLower(parsed.Hostname())
	if !matchableSSHDomainHost(hostname) {
		return "", errInvalidForgejoSSHDomain
	}
	if ip := net.ParseIP(hostname); ip != nil {
		// One spelling per address, so two ways of writing the same IPv6 address
		// still compare equal.
		return ip.String(), nil
	}
	return hostname, nil
}

func ForgejoSSHDomainMatchesHost(resolvedHost, normalizedDomain string) bool {
	if normalizedDomain == "" {
		return false
	}
	return canonicalSSHHost(resolvedHost) == canonicalSSHHost(normalizedDomain)
}

func canonicalSSHHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if inner, bracketed := strings.CutPrefix(host, "["); bracketed {
		if end := strings.Index(inner, "]"); end >= 0 {
			inner = inner[:end]
		}
		host = inner
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	stripped := stripPort(host)
	if ip := net.ParseIP(stripped); ip != nil {
		return ip.String()
	}
	return stripped
}

func matchableSSHDomainHost(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		// A hyphen and an underscore may join characters inside a label but
		// never begin or end one. Underscores earn their place because internal
		// DNS and an ssh -G HostName directive both hand back such names
		// verbatim, and the value has to compare equal to one of those.
		if isLabelJoiner(label[0]) || isLabelJoiner(label[len(label)-1]) {
			return false
		}
		for i := 0; i < len(label); i++ {
			char := label[i]
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && !isLabelJoiner(char) {
				return false
			}
		}
	}
	return true
}

func isLabelJoiner(char byte) bool { return char == '-' || char == '_' }

func ForgejoRemoteSchemeSupported(scheme string) bool {
	switch strings.ToLower(scheme) {
	case "http", "https", "ssh":
		return true
	default:
		return false
	}
}

func glabKnowsHost(host string) bool {
	path := glabConfigPath()
	if path == "" {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var cfg struct {
		Hosts map[string]struct {
			APIHost string `yaml:"api_host"`
		} `yaml:"hosts"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return false
	}
	host = strings.ToLower(host)
	for key, h := range cfg.Hosts {
		if strings.ToLower(strings.TrimSpace(key)) == host {
			return true
		}
		if api := strings.ToLower(strings.TrimSpace(h.APIHost)); api != "" && ExtractHost(api) == host {
			return true
		}
	}
	return false
}

func glabConfigPath() string {
	if dir := os.Getenv("GLAB_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "config.yml")
	}
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "glab-cli", "config.yml")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "glab-cli", "config.yml")
}

func ghKnowsHost(host string) bool {
	path := ghConfigPath()
	if path == "" {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var hosts map[string]interface{}
	if err := yaml.Unmarshal(data, &hosts); err != nil {
		return false
	}
	host = strings.ToLower(host)
	for key := range hosts {
		if strings.ToLower(strings.TrimSpace(key)) == host {
			return true
		}
	}
	return false
}

func ghConfigPath() string {
	if dir := os.Getenv("GH_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "hosts.yml")
	}
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "gh", "hosts.yml")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "gh", "hosts.yml")
}

func teaKnowsHost(host string) bool {
	account := ResolveTeaAccount(host)
	return account.Login != "" || account.Instance != ""
}

type TeaAccount struct {
	Login    string // tea 0.x: logins[].name in config.yml
	Instance string // tea 1.0: a hosts.yml key, e.g. https://gitea.example.com
}

func ResolveTeaAccount(host string) TeaAccount {
	login, _ := teaLoginForHost(host)
	return TeaAccount{Login: login, Instance: teaInstanceForHost(host)}
}

func teaInstanceForHost(host string) string {
	path := teaHostsPath()
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var cfg struct {
		Hosts map[string]struct {
			SSHHost string `yaml:"ssh_host"`
		} `yaml:"hosts"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return ""
	}
	host = strings.ToLower(host)
	match := ""
	for instance, entry := range cfg.Hosts {
		sshHost := strings.ToLower(strings.TrimSpace(stripPort(entry.SSHHost)))
		if ExtractHost(instance) != host && (sshHost == "" || sshHost != host) {
			continue
		}
		if match != "" {
			return ""
		}
		match = instance
	}
	return match
}

func teaLoginForHost(host string) (string, bool) {
	path := teaConfigPath()
	if path == "" {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var cfg struct {
		Logins []struct {
			Name    string `yaml:"name"`
			URL     string `yaml:"url"`
			SSHHost string `yaml:"ssh_host"`
		} `yaml:"logins"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return "", false
	}
	host = strings.ToLower(host)
	for _, login := range cfg.Logins {
		if url := strings.ToLower(strings.TrimSpace(login.URL)); url != "" && ExtractHost(url) == host {
			return login.Name, true
		}
		if sshHost := strings.ToLower(strings.TrimSpace(stripPort(login.SSHHost))); sshHost != "" && sshHost == host {
			return login.Name, true
		}
	}
	return "", false
}

func teaConfigPath() string {
	dir := teaDefaultConfigDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "config.yml")
}

func teaHostsPath() string {
	dir := os.Getenv("TEA_CONFIG_DIR")
	if dir == "" {
		dir = teaDefaultConfigDir()
	}
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "hosts.yml")
}

func teaDefaultConfigDir() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "tea")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "tea")
}

func (p Provider) CLIName() string {
	switch p {
	case ProviderGitHub:
		return "gh"
	case ProviderGitLab:
		return "glab"
	case ProviderBitbucket:
		return "bb"
	case ProviderAzureDevOps:
		return "az"
	case ProviderForgejo:
		return "forgejo-axi"
	case ProviderGitea:
		return "tea"
	case ProviderOrigin:
		return "origin"
	default:
		return ""
	}
}

func (p Provider) AuthCheckCommand() []string {
	switch p {
	case ProviderGitHub:
		return []string{"gh", "auth", "status"}
	case ProviderGitLab:
		return []string{"glab", "auth", "status"}
	case ProviderBitbucket:
		return []string{"bb", "profile", "which"}
	case ProviderAzureDevOps:
		return []string{"az", "account", "show"}
	case ProviderForgejo:
		return []string{"forgejo-axi", "status", "--json"}
	case ProviderGitea:
		// `tea whoami` is gone in tea 1.0; an authenticated GET /user works
		// in both generations.
		return []string{"tea", "api", "/user"}
	case ProviderOrigin:
		return []string{"origin", "auth", "status"}
	default:
		return nil
	}
}

func CLIAvailable(provider Provider) bool {
	name := provider.CLIName()
	if name == "" {
		return false
	}
	_, err := exec.LookPath(name)
	return err == nil
}

func AuthConfigured(ctx context.Context, provider Provider, workDir string) bool {
	args := provider.AuthCheckCommand()
	if len(args) == 0 {
		return false
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = workDir
	winproc.Harden(cmd)
	return cmd.Run() == nil
}
