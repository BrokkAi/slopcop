package gitx

import (
	"fmt"
	"net/url"
	"strings"
)

// OriginURL returns the origin remote URL configured in root.
func OriginURL(root string) (string, error) {
	out, err := Run(root, "remote", "get-url", "origin")
	if err != nil {
		return "", fmt.Errorf("no origin remote is configured; the check service needs a repository URL")
	}
	if out == "" {
		return "", fmt.Errorf("the origin remote has no URL")
	}
	return out, nil
}

// CanonicalRepoURL converts a git remote into the https form the service
// accepts. Git's scp-like syntax has no unambiguous host validation, so the
// client normalizes it once, here, and never sends credentials.
func CanonicalRepoURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("empty repository URL")
	}
	host, path, err := splitRemote(raw)
	if err != nil {
		return "", fmt.Errorf("cannot parse the origin remote URL")
	}
	host = strings.ToLower(strings.TrimSpace(host))
	path = strings.TrimSuffix(strings.Trim(strings.TrimSpace(path), "/"), ".git")
	if host == "" || path == "" {
		return "", fmt.Errorf("cannot parse the origin remote URL")
	}
	return "https://" + host + "/" + path, nil
}

func splitRemote(raw string) (host, path string, err error) {
	if strings.Contains(raw, "://") {
		parsed, parseErr := url.Parse(raw)
		if parseErr != nil || parsed.Hostname() == "" {
			return "", "", fmt.Errorf("cannot parse the origin remote URL")
		}
		return parsed.Hostname(), parsed.Path, nil
	}
	// scp-like syntax: [user@]host:path
	colon := strings.Index(raw, ":")
	if colon <= 0 || strings.Contains(raw[:colon], "/") {
		return "", "", fmt.Errorf("cannot parse the origin remote URL")
	}
	if at := strings.LastIndex(raw[:colon], "@"); at >= 0 {
		return raw[at+1 : colon], raw[colon+1:], nil
	}
	return raw[:colon], raw[colon+1:], nil
}

// UpstreamBase returns merge-base(HEAD, @{upstream}). The boolean is false
// when the branch has no upstream.
func UpstreamBase(root string) (string, bool, error) {
	upstream, err := Run(root, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	if err != nil || upstream == "" {
		return "", false, nil
	}
	return MergeBase(root, "HEAD", upstream)
}

// OriginHeadBase returns merge-base(HEAD, origin/HEAD) when the remote default
// branch is known locally. It is the fallback base for a branch with no
// upstream, such as a fresh CI checkout.
func OriginHeadBase(root string) (string, bool, error) {
	head, err := Run(root, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	if err != nil || head == "" {
		return "", false, nil
	}
	return MergeBase(root, "HEAD", head)
}

// MergeBase returns the merge base of two revisions. The boolean is false when
// git cannot name one, which the caller treats as "fall back".
func MergeBase(root, a, b string) (string, bool, error) {
	out, err := Run(root, "merge-base", a, b)
	if err != nil || out == "" {
		return "", false, nil
	}
	return out, true, nil
}
