package web

import (
	"fmt"
	"net/url"
	"strings"
)

// projectURL is k8s-status's own home, regardless of where this binary is
// actually deployed from (the mirror, a fork, etc.).
const projectURL = "https://github.com/ntmggr/k8s-status"

// repoWebURL converts a git remote as ArgoCD stores it into a browsable URL.
// Handles scp-style SSH ("git@host:group/proj.git"), ssh:// and https:// forms.
// The "git-ssh." host prefix used for cloning is not browsable, so it is dropped.
// Returns "" when the input cannot be understood, so callers render plain text.
func repoWebURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	var host, path string
	switch {
	case strings.HasPrefix(raw, "https://"), strings.HasPrefix(raw, "http://"):
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return ""
		}
		host, path = u.Host, u.Path
	case strings.HasPrefix(raw, "ssh://"):
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return ""
		}
		host, path = u.Hostname(), u.Path
	default:
		at := strings.Index(raw, "@")
		colon := strings.Index(raw, ":")
		if at < 0 || colon < at {
			return ""
		}
		host, path = raw[at+1:colon], raw[colon+1:]
	}

	// The clone host is not the browsable host: git-ssh.<domain> serves SSH,
	// git.<domain> serves the web UI.
	if rest, ok := strings.CutPrefix(host, "git-ssh."); ok {
		host = "git." + rest
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if host == "" || path == "" {
		return ""
	}
	return "https://" + host + "/" + path
}

// repoTreeURL links to a branch or tag of the repository.
func repoTreeURL(raw, rev string) string {
	base := repoWebURL(raw)
	if base == "" || rev == "" {
		return ""
	}
	return fmt.Sprintf("%s/-/tree/%s", base, url.PathEscape(rev))
}

// repoCommitURL links to a specific commit.
func repoCommitURL(raw, sha string) string {
	base := repoWebURL(raw)
	if base == "" || sha == "" {
		return ""
	}
	return fmt.Sprintf("%s/-/commit/%s", base, url.PathEscape(sha))
}

// releaseURL links k8s-status's own build version to its GitHub release,
// regardless of where this binary is actually deployed from. "dev" is a
// local/unreleased build with no matching release.
func releaseURL(version string) string {
	if version == "" || version == "dev" {
		return ""
	}
	return projectURL + "/releases/tag/v" + url.PathEscape(version)
}
