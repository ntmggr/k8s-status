package web

import "testing"

func TestRepoWebURL(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"scp style with git-ssh prefix", "git@git-ssh.example.com:devops/k8s/helm-charts/k8s-status.git", "https://git.example.com/devops/k8s/helm-charts/k8s-status"},
		{"scp style plain host", "git@example.com:group/proj.git", "https://example.com/group/proj"},
		{"ssh scheme", "ssh://git@git-ssh.example.com/group/proj.git", "https://git.example.com/group/proj"},
		{"https passthrough", "https://example.com/group/proj.git", "https://example.com/group/proj"},
		{"https without suffix", "https://example.com/group/proj", "https://example.com/group/proj"},
		{"empty", "", ""},
		{"garbage", "not a url", ""},
		{"missing path", "git@example.com:", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := repoWebURL(c.in); got != c.want {
				t.Errorf("repoWebURL(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestRepoTreeAndCommitURL(t *testing.T) {
	repo := "git@git-ssh.example.com:group/proj.git"
	if got, want := repoTreeURL(repo, "develop"), "https://git.example.com/group/proj/-/tree/develop"; got != want {
		t.Errorf("tree = %q, want %q", got, want)
	}
	if got, want := repoCommitURL(repo, "abc123"), "https://git.example.com/group/proj/-/commit/abc123"; got != want {
		t.Errorf("commit = %q, want %q", got, want)
	}
	if got := repoTreeURL("garbage", "develop"); got != "" {
		t.Errorf("tree on bad repo = %q, want empty", got)
	}
	if got := repoCommitURL(repo, ""); got != "" {
		t.Errorf("commit with no sha = %q, want empty", got)
	}
}

func TestReleaseURL(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"released version", "0.19.3", "https://github.com/ntmggr/k8s-status/releases/tag/v0.19.3"},
		{"dev build", "dev", ""},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := releaseURL(c.in); got != c.want {
				t.Errorf("releaseURL(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
