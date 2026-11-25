package gitx

import (
	"fmt"
	"slices"
	"strings"
)

// ReviewedTrees returns validated tree approvals, never commit or malformed refs.
func ReviewedTrees(root string) (map[string]bool, error) {
	out, err := Run(root, "for-each-ref", "--format=%(refname:strip=3) %(objectname)", "refs/slopcop/reviewed/")
	if err != nil {
		return nil, err
	}
	trees := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 || f[0] != f[1] {
			continue
		}
		typ, err := Run(root, "cat-file", "-t", f[1])
		if err == nil && typ == "tree" {
			trees[f[1]] = true
		}
	}
	return trees, nil
}

// IsDocumentationOnlyDiff checks format, location, mode and blob content independently.
func IsDocumentationOnlyDiff(root, baseTree, headTree string) (bool, error) {
	flags := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--ignore-submodules=none"}
	diff := func(format string, paths []string) ([]byte, error) {
		args := append(append([]string{}, flags...), format, "-z", baseTree, headTree, "--")
		return RunWithoutRepoAttributes(root, append(args, paths...)...)
	}
	blocked := []string{"**/AGENTS.md", "**/AGENTS.override.md", "**/CLAUDE.md", "**/CLAUDE.local.md", "**/GEMINI.md", "**/SKILL.md", "**/*.instructions.md", "**/*.prompt.md", "**/.agents/*.md", "**/.agents/skills/**", "**/.github/**", "**/.claude/**", "**/.codex/**", "**/.gemini/**", "**/.cursor/**", "**/.opencode/**", "**/.slopcop/**", "**/.bifrostignore"}
	paths := []string{}
	for _, p := range blocked {
		paths = append(paths, ":(top,glob)"+p)
	}
	out, err := diff("--name-only", paths)
	if err != nil {
		return false, err
	}
	if len(out) > 0 {
		return false, nil
	}
	for _, allowed := range [][]string{
		{"**/*.md", "**/*.markdown", "**/*.rst", "**/*.adoc", "**/*.asciidoc", "READM[E]", "LICENS[E]", "COPYIN[G]", "AUTHOR[S]", "CONTRIBUTOR[S]", "CHANGELO[G]", "NEW[S]"},
		{"**/docs/**", "**/doc/**", "**/.agents/plans/**", "**/READM[E]*", "**/LICENS[E]*", "**/COPYIN[G]*", "**/AUTHOR[S]*", "**/CONTRIBUTOR[S]*", "**/CHANGELO[G]*", "**/NEW[S]*", "**/CONTRIBUTIN[G]*", "**/CODE_OF_CONDUC[T]*", "**/SECURIT[Y]*"},
	} {
		paths = []string{":(top)**"}
		for _, p := range allowed {
			paths = append(paths, ":(top,exclude,glob)"+p)
		}
		out, err = diff("--name-only", paths)
		if err != nil {
			return false, err
		}
		if len(out) > 0 {
			return false, nil
		}
	}
	out, err = diff("--raw", nil)
	if err != nil {
		return false, err
	}
	entries := strings.Split(string(out), "\x00")
	for i := 0; i+1 < len(entries); i += 2 {
		f := strings.Fields(entries[i])
		if len(f) < 2 || (f[0] != ":000000" && f[0] != ":100644") || (f[1] != "000000" && f[1] != "100644") {
			return false, nil
		}
	}
	out, err = diff("--numstat", nil)
	if err != nil {
		return false, err
	}
	for _, entry := range strings.Split(string(out), "\x00") {
		if strings.HasPrefix(entry, "-\t-\t") {
			return false, nil
		}
	}
	return true, nil
}

// PushDestination describes the single HEAD push whose exemption is being proved.
type PushDestination struct {
	Remote          string
	DestRef         string
	AllowExemptions bool
}

// gateDestination uses only fetched state belonging to the actual push remote.
func gateDestination(root string, dest PushDestination) (upstream, remote string, trusted, valid bool) {
	if !dest.AllowExemptions {
		return
	}
	branch := CurrentBranch(root)
	remote = dest.Remote
	for _, key := range []string{"branch." + branch + ".pushRemote", "remote.pushDefault", "branch." + branch + ".remote"} {
		if remote == "" {
			remote, _ = Run(root, "config", "--get", key)
		}
	}
	if remote == "" {
		remote = "origin"
	}
	// Broad configured push refspecs cannot establish a single HEAD destination.
	if dest.DestRef == "" {
		if specs, _ := Run(root, "config", "--get-all", "remote."+remote+".push"); specs != "" {
			return "", remote, false, false
		}
		mode, _ := Run(root, "config", "--get", "push.default")
		if mode != "" && mode != "simple" && mode != "current" && mode != "upstream" {
			return "", remote, false, false
		}
		if mode == "current" {
			dest.DestRef = branch
		}
	}
	if mirror, _ := Run(root, "config", "--bool", "--get", "remote."+remote+".mirror"); mirror == "true" {
		return "", remote, false, false
	}
	// get-url accepts URLs too; require a configured remote and a single push URL.
	configured, _ := Run(root, "remote")
	if !slices.Contains(strings.Fields(configured), remote) {
		return "", remote, false, false
	}
	fetch, fe := Run(root, "remote", "get-url", "--all", remote)
	push, pe := Run(root, "remote", "get-url", "--push", "--all", remote)
	trusted = fe == nil && pe == nil && fetch != "" && fetch == push && !strings.Contains(fetch, "\n")
	if !trusted {
		return "", remote, false, false
	}
	ref := ""
	if dest.DestRef != "" {
		if _, err := Run(root, "check-ref-format", "refs/heads/"+dest.DestRef); err != nil {
			return "", remote, trusted, false
		}
		ref = "refs/remotes/" + remote + "/" + dest.DestRef
	} else {
		upstreamRef, e := Run(root, "rev-parse", "--symbolic-full-name", "@{upstream}")
		if e == nil && strings.HasPrefix(upstreamRef, "refs/remotes/"+remote+"/") {
			ref = upstreamRef
		} else {
			ref = "refs/remotes/" + remote + "/" + branch
			configuredRemote, _ := Run(root, "config", "--get", "branch."+branch+".remote")
			merge, _ := Run(root, "config", "--get", "branch."+branch+".merge")
			if configuredRemote == remote && merge != "" {
				return "", remote, trusted, false
			}
		}
	}
	upstream, _ = ResolveCommit(root, ref)
	return upstream, remote, trusted, true
}

func reviewedAncestors(root string, trees map[string]bool, revisions ...string) ([]string, error) {
	out, err := Run(root, append([]string{"rev-list", "--format=%H %T"}, revisions...)...)
	if err != nil {
		return nil, err
	}
	var commits []string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && trees[f[1]] {
			commits = append(commits, f[0])
		}
	}
	return commits, nil
}

// IsDocumentationOnlyPush compares whole trees against eligible published or
// reviewed history, retaining the destination's history for existing branches.
func IsDocumentationOnlyPush(root, headCommit, headTree string, reviewedTrees map[string]bool, dest PushDestination) (bool, error) {
	upstream, remote, trusted, valid := gateDestination(root, dest)
	if !valid || (upstream != "" && !IsAncestor(root, upstream, headCommit)) {
		return false, nil
	}
	revisions := []string{headCommit}
	var bases []string
	if upstream != "" {
		ok, err := IsDocumentationOnlyDiff(root, upstream+"^{tree}", headTree)
		if err != nil || ok {
			return ok, err
		}
		revisions = append(revisions, "^"+upstream)
		tree, err := Run(root, "rev-parse", upstream+"^{tree}")
		if err != nil {
			return false, err
		}
		if reviewedTrees[tree] {
			bases = append(bases, upstream)
		}
	}
	commits, err := reviewedAncestors(root, reviewedTrees, revisions...)
	if err != nil {
		return false, err
	}
	for _, commit := range commits {
		if upstream == "" || IsAncestor(root, upstream, commit) {
			bases = append(bases, commit)
		}
	}
	if upstream == "" && trusted {
		out, err := Run(root, "for-each-ref", "--format=%(objectname)", "refs/remotes/"+remote+"/")
		if err != nil {
			return false, err
		}
		for _, ref := range strings.Fields(out) {
			if IsAncestor(root, ref, headCommit) {
				bases = append(bases, ref)
			}
		}
	}
	if upstream == "" && len(bases) == 0 {
		bases = append(bases, "4b825dc642cb6eb9a060e54bf8d69288fbee4904")
	}
	for _, base := range bases {
		ok, err := IsDocumentationOnlyDiff(root, base, headTree)
		if err != nil || ok {
			return ok, err
		}
	}
	return false, nil
}

// IsCleanUpstreamMergePush proves both the reviewed baseline merge and the
// actual parent merge produce exactly HEAD, without conflict resolutions.
func IsCleanUpstreamMergePush(root, headCommit, headTree string, reviewedTrees map[string]bool, dest PushDestination) (bool, error) {
	out, err := Run(root, "rev-list", "--parents", "-n", "1", headCommit)
	if err != nil {
		return false, nil
	}
	parents := strings.Fields(out)
	if len(parents) != 3 {
		return false, nil
	}
	p1, p2 := parents[1], parents[2]
	upstream, remote, trusted, valid := gateDestination(root, dest)
	if !valid || (upstream != "" && !IsAncestor(root, upstream, headCommit)) {
		return false, nil
	}
	if upstream == "" || p2 != upstream {
		if !trusted {
			return false, nil
		}
		published := false
		for _, suffix := range []string{"HEAD", "main", "master"} {
			ref, e := ResolveCommit(root, "refs/remotes/"+remote+"/"+suffix)
			if e == nil {
				published = IsAncestor(root, p2, ref)
				break
			}
		}
		if !published {
			return false, nil
		}
	}
	candidates, err := reviewedAncestors(root, reviewedTrees, p1, "^"+p2)
	if err != nil || len(candidates) == 0 {
		return false, nil
	}
	independent, err := Run(root, append([]string{"merge-base", "--independent"}, candidates...)...)
	if err != nil {
		return false, nil
	}
	candidates = strings.Fields(independent)
	if len(candidates) != 1 {
		return false, nil
	}
	// Older merge-tree implementations lack the safety fixes required here.
	version, err := Run(root, "version")
	var major, minor, patch int
	if err != nil {
		return false, nil
	}
	if n, _ := fmt.Sscanf(version, "git version %d.%d.%d", &major, &minor, &patch); n != 3 || major < 2 || (major == 2 && (minor < 39 || (minor == 39 && patch < 5))) {
		return false, nil
	}
	// Unsupported versions/options and conflicts both fail closed.
	for _, base := range []string{candidates[0], p1} {
		tree, e := Run(root, "merge-tree", "--write-tree", "--allow-unrelated-histories", "--no-messages", base, p2)
		if e != nil || tree != headTree {
			return false, nil
		}
	}
	return true, nil
}
