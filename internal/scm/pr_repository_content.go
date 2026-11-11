package scm

import (
	"context"
	"net"
	"net/url"
	"strings"
)

type RepositoryIdentity struct {
	Host string
	Path string
}

type PRRepositoryContent struct {
	Content    PRContent
	Repository RepositoryIdentity
}

type PRRepositoryContentReader interface {
	GetPRRepositoryContent(context.Context, string) (PRRepositoryContent, error)
}

func RepositoryIdentityFromURL(raw string) RepositoryIdentity {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return RepositoryIdentity{}
	}
	host := strings.ToLower(u.Hostname())
	if port := u.Port(); port != "" && !(u.Scheme == "https" && port == "443") && !(u.Scheme == "http" && port == "80") {
		host = net.JoinHostPort(host, port)
	}
	return RepositoryIdentity{Host: host, Path: strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")}
}

func RepositoryIdentityFromPRURL(raw, route, number string) RepositoryIdentity {
	if raw == "" || number == "" || !strings.HasSuffix(raw, route+number) {
		return RepositoryIdentity{}
	}
	return RepositoryIdentityFromURL(strings.TrimSuffix(raw, route+number))
}
