package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"

	"github.com/BrokkAi/slopcop/internal/gitx"
	"github.com/spf13/cobra"
)

// shellSeparator splits a command line at the operators that start a new
// command: ;, &, &&, |, ||, newlines, subshells and command substitution.
var shellSeparator = regexp.MustCompile("[;&|\n()`]")

// shellWord is one shell word, quoted or not.
const shellWord = `(?:"[^"]*"|'[^']*'|\S+)`

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

// forgedApproval reports whether a command names SlopCop's approval refs. Only
// slopcop check and slopcop override write them, and neither is run with the
// ref on its command line, so an agent's command that names one is most
// likely writing an approval the check did not give (git update-ref, or a
// file under .git/refs). Reading them is blocked too: an agent has no need to.
// Like pushCommand, this guards against an agent's shortcut, not an attacker.
func forgedApproval(command string) bool {
	return approvalRef.MatchString(command)
}

// forgedApprovalText is the reason a command that names the approval refs is
// blocked.
const forgedApprovalText = "slopcop: blocked: refs/slopcop/ holds the approvals that slopcop check and slopcop override write, and nothing else may write them. Run `slopcop check` until it approves the tree, or for review findings that are wrong run `slopcop override --reason \"...\"`"

// shellTools are the tool names a PreToolUse payload gives a shell command:
// Bash from Claude Code and Codex, and run_terminal_command from Grok, which
// runs the gate from the repository's Claude Code settings.
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

// gateTree allows a push only when the committed HEAD tree has an approval ref.
// Hooks pass outside repositories because they can fire for unrelated work.
func gateTree(root string) error {
	root, err := gitx.Toplevel(root)
	if err != nil {
		return nil
	}
	tree, err := gitx.Run(root, "rev-parse", "--verify", "HEAD^{tree}")
	if err != nil {
		return nil
	}
	reviewed, err := gitx.Run(root, "rev-parse", "--verify", "refs/slopcop/reviewed/"+tree)
	if err == nil && reviewed == tree {
		return nil
	}
	return fmt.Errorf("slopcop: push blocked: HEAD's tree %s has no passing slopcop check. The check approves exactly the content it saw: commit the checked state unchanged (or check the committed state), run `slopcop check` until it approves the tree (see the slopcop skill), or for review findings that are wrong run `slopcop override --reason \"...\"`; then push", tree)
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
				if hook != "claude" && hook != "codex" {
					return fmt.Errorf("unknown gate hook %q", hook)
				}
				var input gateInput
				decoder := json.NewDecoder(io.LimitReader(cmd.InOrStdin(), (4<<20)+1))
				if err := decoder.Decode(&input); err != nil {
					return gateBlock(cmd, fmt.Errorf("slopcop: invalid %s hook input: %w", hook, err))
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
				root = input.CWD
			}
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
