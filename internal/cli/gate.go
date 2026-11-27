package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/gitx"
	"github.com/BrokkAi/slopcop/internal/review"
	"github.com/spf13/cobra"
)

// shellSeparator splits a command line at the operators that start a new
// command: ;, &, &&, |, ||, newlines, subshells and command substitution.
var shellSeparator = regexp.MustCompile("[;&|\n()`]")

// shellWord is one shell word: quoted parts, backslash escapes such as
// `my\ repo`, and plain characters, run together.
const shellWord = `(?:"[^"]*"|'[^']*'|\\.|[^\s"'\\])+`

// gitPush matches git whose subcommand is push, allowing git's global options
// before it: -C dir and -c key=value, the long options that take a separate
// value, flags such as --no-pager or --git-dir=dir, and -p, -P, -v and -h. Any other word before
// push is a different subcommand (git commit -m "push", git stash push).
var gitPush = regexp.MustCompile(`\bgit(?:\s+(?:-[Cc]\s+` + shellWord + `|--(?:git-dir|work-tree|namespace|config-env)\s+` + shellWord + `|--[-\w]+(?:=` + shellWord + `)?|-[pPvh]))*\s+push(?:\s|$)`)

// ghPRCreate matches gh pr create.
var ghPRCreate = regexp.MustCompile(`\bgh\b[^\n;&|]*\bpr\s+create\b`)

// pushCommand recognizes the git push and gh pr create forms that checkpoint
// guarded, in any command of a chained command line. It is deliberately
// conservative: this is a prompt guard, not a shell parser or a security
// boundary, so a quoted "git push" inside another command still counts.
func pushCommand(command string) bool {
	for _, part := range shellSeparator.Split(command, -1) {
		if gitPush.MatchString(part) || ghPRCreate.MatchString(part) {
			return true
		}
	}
	return false
}

// approvalRef matches a mention of SlopCop's approval refs, as a ref name or
// as a path under .git, with either path separator.
var approvalRef = regexp.MustCompile(`refs[/\\]+slopcop\b`)

// reviewStatePath matches the repository-local state directory that arms the
// push gate. Removing it would turn the gate off without an approval.
var reviewStatePath = regexp.MustCompile(`\.git[/\\]+slopcop(?:[/\\]|\b)`)

// forgedApproval reports whether a command names SlopCop's approval refs. Only
// slopcop check and slopcop override write them, and neither is run with the
// ref on its command line, so an agent's command that names one is most
// likely writing an approval the check did not give (git update-ref, or a
// file under .git/refs). It also names the state directory that arms the
// user-level gate so a command cannot simply disable enforcement. Reading
// either is blocked too: an agent has no need to. Like pushCommand, this
// guards against an agent's shortcut, not an attacker.
func forgedApproval(command string) bool {
	return approvalRef.MatchString(command) || reviewStatePath.MatchString(command)
}

// forgedApprovalText is the reason a command that names the approval refs is
// blocked.
const forgedApprovalText = "slopcop: blocked: SlopCop owns refs/slopcop/ and .git/slopcop state; nothing else may write or remove them. Run `slopcop check` until it approves the tree, or for review findings that are wrong, or a static analyzer run that keeps failing, run `slopcop override --reason \"...\"`"

// unsureTargetText is the reason a push is blocked when the gate cannot read
// which repository it would push, by why.
var unsureTargetText = map[string]string{
	unsureShell:   "slopcop: push blocked: the gate cannot tell which repository this push runs in, because a directory on the command line depends on the shell ($VAR, $(...), cd -, popd). Push with a path it can read: run it from the repository, or use `cd <path> && git push` or `git -C <path> push`",
	unsureMissing: "slopcop: push blocked: a directory this push runs in does not exist yet, so the gate cannot judge what it would push; the same command makes it, as git worktree add, git clone or cp -r do. Make the directory first, then push from it as a command of its own",
}

// shellTools are the tool names a PreToolUse payload gives a shell command:
// Bash from Claude Code, Codex and Copilot CLI (which names its shell tool
// Bash for a hook under Claude Code's event names), and run_terminal_command
// from Grok, which runs the gate from the repository's Claude Code settings.
var shellTools = map[string]bool{"Bash": true, "run_terminal_command": true}

// githubPushTool matches the GitHub MCP tools that open a pull request or
// push files without git, under any server name.
var githubPushTool = regexp.MustCompile(`^mcp__.+__(create_pull_request|push_files)$`)

// gateInput is the part of a PreToolUse payload needed to identify a push and
// the working directory from which it would run.
type gateInput struct {
	CWD       string `json:"cwd"`
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		Command string `json:"command"`
	} `json:"tool_input"`
}

// armedRoot is the checkout that holds dir, and whether SlopCop's review
// store there arms the gate.
func armedRoot(dir string) (string, bool) {
	root, err := gitx.Toplevel(dir)
	if err != nil {
		return "", false
	}
	gitDir, err := gitx.Run(root, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", false
	}
	if _, err := os.Stat(filepath.Join(strings.TrimSpace(gitDir), "slopcop")); err != nil {
		return "", false
	}
	return root, true
}

// gateArmed reports whether the gate guards the checkout that holds dir.
func gateArmed(dir string) bool {
	_, ok := armedRoot(dir)
	return ok
}

// gateTree allows a push only when the committed HEAD tree has an approval ref.
// User-level hooks fire in every repository, so a checkout that has never run
// SlopCop is left alone. The review store is created before a check starts; its
// presence arms the gate, and after that only an approved HEAD tree may push.
func gateTree(root string) error {
	root, ok := armedRoot(root)
	if !ok {
		return nil
	}
	tree, err := gitx.Run(root, "rev-parse", "--verify", "HEAD^{tree}")
	if err != nil {
		return nil
	}
	reviewed, err := gitx.Run(root, "rev-parse", "--verify", "refs/slopcop/reviewed/"+tree)
	if err == nil && reviewed == tree {
		return reviewOnlyStillStands(root, tree)
	}
	return fmt.Errorf("slopcop: push blocked: HEAD's tree %s in %s has no passing slopcop check. The check approves exactly the content it saw: commit the checked state unchanged (or check the committed state), run `slopcop check` in %s until it approves the tree (see the slopcop skill), or for review findings that are wrong, or a static analyzer run that keeps failing, run `slopcop override --reason \"...\"` there; then push", tree, root, root)
}

// reviewOnlyStillStands refuses an approval that a review alone gave, from
// `--mode ai`, once the static half can run: the checkout has a login and an
// origin with a branch. A review-only check approves a tree only while the
// static half cannot run, and logging out for one check, or the first push
// landing, must not leave that approval standing. It asks origin only for
// such an approval with a login present.
func reviewOnlyStillStands(root, tree string) error {
	reviewOnly, err := review.ReviewedWithoutStatic(root, tree)
	if err != nil {
		return fmt.Errorf("slopcop: push blocked: cannot read how HEAD's tree %s in %s was approved: %w", tree, root, err)
	}
	if !reviewOnly {
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("slopcop: push blocked: HEAD's tree %s in %s was approved by the review alone, and the config file cannot be read to tell whether the static check can run: %w", tree, root, err)
	}
	if available, _ := staticAvailability(context.Background(), cfg, "", root); !available {
		return nil
	}
	return fmt.Errorf("slopcop: push blocked: HEAD's tree %s in %s was approved by the review alone (--mode ai), and this repository can now run the static check. Run `slopcop check` in %s so both halves check it, then push", tree, root, root)
}

// newGateCommand exposes a direct tree check and a hidden hook form. The hook
// inspects shell push commands and the GitHub MCP tools that push or open a
// pull request, and reads at most four MiB so a malformed hook cannot consume
// an unbounded stream. Invalid payloads block conservatively, as checkpoint
// did.
func newGateCommand() *cobra.Command {
	var root, hook string
	command := &cobra.Command{
		Use:   "gate",
		Short: "Check whether HEAD's tree passed SlopCop",
		Long: `Check whether the current committed tree has a passing SlopCop check.
A Claude Code or Codex PreToolUse hook uses this command to block git push,
gh pr create, and GitHub MCP tools that push or open a pull request, until the
exact committed tree is reviewed. The hook reads its payload
from stdin and blocks malformed input rather than allowing an unchecked push.`,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if hook != "" {
				if hook != "claude" && hook != "codex" && hook != "grok" && hook != "copilot" {
					return fmt.Errorf("unknown gate hook %q", hook)
				}
				// Grok also runs the Claude Code compatibility settings. Its
				// own hook performs the same gate, so skip the duplicate.
				if hook == "claude" && os.Getenv("GROK_HOOK_EVENT") != "" {
					return nil
				}
				// Copilot CLI runs a repository's Claude Code settings too.
				// When its own gate is installed, that one judges the push;
				// otherwise this one does, as the payloads are alike.
				if hook == "claude" && underCopilotHook() && copilotHookInstalled(true) {
					return nil
				}
				var input gateInput
				decoder := json.NewDecoder(io.LimitReader(cmd.InOrStdin(), (4<<20)+1))
				if err := decoder.Decode(&input); err != nil {
					return gateBlock(cmd, fmt.Errorf("slopcop: invalid %s hook input: %w", hook, err))
				}
				if input.CWD != "" {
					_ = cleanupInstalledLegacyRepository(legacyRepoRoot(input.CWD))
				}
				switch {
				case shellTools[input.ToolName]:
					if forgedApproval(input.ToolInput.Command) {
						return gateBlock(cmd, errors.New(forgedApprovalText))
					}
					if !pushCommand(input.ToolInput.Command) {
						return nil
					}
				case githubPushTool.MatchString(input.ToolName):
				default:
					return nil
				}
				if input.CWD == "" {
					return gateBlock(cmd, fmt.Errorf("slopcop: %s hook input is missing cwd", hook))
				}
				// A shell push runs where its cd, git -C and --git-dir put
				// it; an MCP push from the session's directory.
				roots := []string{input.CWD}
				if shellTools[input.ToolName] {
					targets, unsure := pushTargets(input.CWD, input.ToolInput.Command)
					// Where SlopCop guards none of the repositories in
					// sight, a push it cannot read is none of its business.
					// In sight are the session's, the pushes' and any the
					// line names, such as the source of a cp -r.
					if unsure != "" && (gateArmed(input.CWD) || slices.ContainsFunc(targets, gateArmed) ||
						slices.ContainsFunc(namedDirs(input.CWD, input.ToolInput.Command), gateArmed)) {
						return gateBlock(cmd, errors.New(unsureTargetText[unsure]))
					}
					if len(targets) > 0 {
						roots = targets
					}
				}
				for _, target := range roots {
					if err := gateTree(target); err != nil {
						return gateBlock(cmd, err)
					}
				}
				return nil
			}
			_ = cleanupInstalledLegacyRepository(legacyRepoRoot(root))
			return gateBlock(cmd, gateTree(root))
		},
	}
	command.Flags().StringVar(&root, "root", ".", "Git repository directory")
	command.Flags().StringVar(&hook, "hook", "", "read a harness PreToolUse payload")
	_ = command.Flags().MarkHidden("hook")
	return command
}

// gateBlock prints a reason and returns exit 2, which Claude Code, Codex and
// Grok read as a blocked PreToolUse call; Codex and Grok need the reason on
// stderr, and Grok shows only its first line, so it is one line. The installed gate handler has no `|| true`
// guard, so this exit reaches Claude unchanged.
func gateBlock(cmd *cobra.Command, err error) error {
	if err == nil {
		return nil
	}
	fmt.Fprintln(cmd.ErrOrStderr(), err)
	return silentExit{2}
}
