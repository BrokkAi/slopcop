package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/gitx"
	"github.com/BrokkAi/slopcop/internal/review"
)

// The hook judges a command line before any of it runs, so in
// `slopcop override --reason "..." && git push` the push would be refused
// although the override before it approves the tree. The gate lets such a
// line through when it can tell, before it runs, that a successful override
// approves exactly the tree the push sends:
//
//   - The line is one or more `slopcop override --reason "..."` commands, then
//     the pushes, every one joined to the next by &&, with nothing else before
//     the last push. && stops the push when the override fails; ; and a
//     newline do not, so they are refused. A command in between could commit
//     or move, and change what is pushed.
//   - The override is the whole-review form, run in the session's directory,
//     which is the repository every push runs in: no finding id (that one
//     approves no tree), --root, --undo or --list.
//   - The latest review there is of HEAD's tree. An override approves only
//     the working tree it finds, and only when the latest review is of that
//     tree, so if it succeeds it approved HEAD's tree.
//
// Anything else is refused as before, with a reason that says to run the
// override as a command of its own. Like pushCommand, this guards against an
// agent's slip; it is not a shell parser or a security boundary.

// mentionsOverride matches a command line that may run slopcop override.
var mentionsOverride = regexp.MustCompile(`\bslopcop\s+override\b`)

// overrideShape is how a command line runs slopcop override around its
// pushes.
type overrideShape int

const (
	// noOverride: the line runs no slopcop override.
	noOverride overrideShape = iota
	// overrideOnly: the line is whole-review overrides and nothing else; the
	// git push the gate saw is in a reason.
	overrideOnly
	// overrideThenPush: overrides joined by && to the pushes, which approve
	// the tree before the pushes run.
	overrideThenPush
	// overrideUncredited: an override the gate cannot count for the push.
	overrideUncredited
)

// overrideBeforePush reads how command runs slopcop override around its
// pushes, and for overrideUncredited, why the override does not count.
func overrideBeforePush(command string) (overrideShape, string) {
	if !mentionsOverride.MatchString(command) {
		return noOverride, ""
	}
	segs := segments(command)
	const (
		blank = iota
		override
		push
		other
	)
	kinds := make([]int, len(segs))
	lastPush := -1
	for i, seg := range segs {
		switch {
		case strings.TrimSpace(seg.text) == "":
			kinds[i] = blank
		case !shellExpands(seg.text) && wholeReviewOverride(shellWords(seg.text)):
			// Read first: its reason may mention git push. An expansion
			// could turn into other flags, such as --list.
			kinds[i] = override
		case plainPush(seg.text):
			kinds[i] = push
			lastPush = i
		default:
			kinds[i] = other
			// A command the gate sees as a push but cannot read as one,
			// such as `command git push`, sh -c, or a git alias, may
			// push: every command up to it has to pass the checks below.
			if gitPush.MatchString(seg.text) || ghPRCreate.MatchString(seg.text) {
				lastPush = i
			}
		}
	}
	const notWholeReview = "the gate counts only `slopcop override --reason \"...\"` itself, without a finding id, --root, --undo or --list"
	if lastPush < 0 {
		for i, kind := range kinds {
			if kind == other || kind == blank && i < len(kinds)-1 {
				return overrideUncredited, notWholeReview
			}
		}
		return overrideOnly, ""
	}
	if kinds[0] != override {
		if kinds[0] == push {
			return overrideUncredited, "the push runs before the override"
		}
		if mentionsOverride.MatchString(segs[0].text) {
			return overrideUncredited, notWholeReview
		}
		return overrideUncredited, "the override must be the first command on the line"
	}
	pushed := false
	for i := 0; i <= lastPush; i++ {
		switch kinds[i] {
		case push:
			pushed = true
		case override:
			if pushed {
				return overrideUncredited, "a push runs before the override"
			}
		case other:
			if mentionsOverride.MatchString(segs[i].text) {
				return overrideUncredited, notWholeReview
			}
			if i == lastPush {
				return overrideUncredited, "the gate reads only git push and gh pr create run directly, not a command that may run one"
			}
			return overrideUncredited, "another command runs between the override and the push, and could change what is pushed"
		default:
			return overrideUncredited, "the gate reads only an override joined to the push by &&"
		}
		if i == lastPush {
			// A substitution in the last push's arguments runs before it.
			if next := segs[i].next; next == "(" || next == "`" {
				return overrideUncredited, "the push runs a command substitution, which could change what is pushed"
			}
			break
		}
		switch segs[i].next {
		case "&&":
		case ";", "\n":
			return overrideUncredited, "after ; or a newline the push runs even when the override fails; join them with &&"
		default:
			return overrideUncredited, "the gate reads only an override joined to the push by &&"
		}
	}
	return overrideThenPush, ""
}

// plainPush reports whether a segment is itself git push or gh pr create:
// git whose subcommand word is push after only global options, or gh pr
// create. Another command that mentions one (sh -c, a git alias, a commit
// message) is not.
func plainPush(text string) bool {
	if !gitPush.MatchString(text) && !ghPRCreate.MatchString(text) {
		return false
	}
	words := shellWords(text)
	if len(words) == 0 {
		return false
	}
	switch filepath.Base(words[0]) {
	case "gh":
		return len(words) >= 3 && words[1] == "pr" && words[2] == "create"
	case "git":
	default:
		return false
	}
	for j := 1; j < len(words); j++ {
		switch word := words[j]; {
		case word == "push":
			return true
		case word == "-C" || word == "-c" || word == "--git-dir" || word == "--work-tree" || word == "--namespace" || word == "--config-env":
			j++
		case strings.HasPrefix(word, "-"):
		default:
			return false
		}
	}
	return false
}

// shellExpands reports whether the shell may expand part of text into other
// words: $ or a backtick outside single quotes, or a brace, glob or tilde
// outside any quotes.
func shellExpands(text string) bool {
	var quote byte
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
		case c == '\\':
			i++
		case quote == '"':
			if c == '"' {
				quote = 0
			} else if c == '$' || c == '`' {
				return true
			}
		case c == '\'' || c == '"':
			quote = c
		case strings.IndexByte("$`{}*?[~", c) >= 0:
			return true
		}
	}
	return false
}

// wholeReviewOverride reports whether words are `slopcop override` accepting
// the latest review with a reason, and nothing the gate cannot read: the
// flags it allows are --reason, --workspace (-w) and --format.
func wholeReviewOverride(words []string) bool {
	if len(words) < 2 || filepath.Base(words[0]) != "slopcop" || words[1] != "override" {
		return false
	}
	reason := ""
	for i := 2; i < len(words); i++ {
		word := words[i]
		name, value, glued := strings.Cut(word, "=")
		switch name {
		case "--reason", "--workspace", "-w", "--format":
		default:
			return false
		}
		if !glued {
			if i+1 >= len(words) {
				return false
			}
			i++
			value = words[i]
		}
		if name == "--reason" {
			reason = value
		}
	}
	return strings.TrimSpace(reason) != ""
}

// overrideApprovesHead says why an override run from cwd would not approve
// the HEAD tree a push from target sends, or nil when it would. The override
// itself still has to succeed; && keeps the push from running if it fails.
func overrideApprovesHead(cwd, target string) error {
	root, ok := armedRoot(target)
	if !ok {
		return nil
	}
	if here, err := gitx.Toplevel(cwd); err != nil || here != root {
		return fmt.Errorf("the override runs in %s, not in %s where the push runs", cwd, root)
	}
	tree, err := gitx.Run(root, "rev-parse", "--verify", "HEAD^{tree}")
	if err != nil {
		return fmt.Errorf("cannot read HEAD's tree: %w", err)
	}
	last, err := review.LastRecord(root)
	if err != nil {
		return fmt.Errorf("cannot read the latest review: %w", err)
	}
	if last == nil {
		return fmt.Errorf("no review has run in %s; run `slopcop check` first", root)
	}
	if last.Capture.Tree != tree {
		return fmt.Errorf("the latest review is of tree %s, not HEAD's tree %s, so the override would not approve what the push sends. Commit the reviewed state unchanged, or run `slopcop check` on the committed state", last.Capture.Tree, tree)
	}
	if last.Mode == "ai" {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("the latest review ran without the static check (--mode ai), and the config file cannot be read to tell whether the static check can run: %w", err)
		}
		if available, _ := staticAvailability(context.Background(), cfg, "", root); available {
			return fmt.Errorf("the latest review ran without the static check (--mode ai), and this repository can run it. Run `slopcop check` so both halves check the tree")
		}
	}
	return nil
}

// overrideOwnCommand is the advice for an override the gate cannot count.
const overrideOwnCommand = "Run `slopcop override --reason \"...\"` as its own command first, then push"
