package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/gitx"
	"github.com/BrokkAi/slopcop/internal/report"
	"github.com/BrokkAi/slopcop/internal/review"
	"github.com/spf13/cobra"
)

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
// boundary, so an unquoted `echo git push` still counts. Text the shell does
// not run as a command does not: a quoted argument (git commit -m "git push",
// a sed script), a heredoc body that is only data, a comment. Code the line
// runs does: bash -c and eval code, a heredoc fed to a shell, a command
// substitution in an unquoted heredoc, a pipe into a shell, and interpreter
// code that spawns a process and names git push.
func pushCommand(command string) bool {
	return pushIn(command, 0)
}

// pushIn is pushCommand for a command line or code a shell in one runs.
func pushIn(command string, depth int) bool {
	if depth > 8 {
		return gitPush.MatchString(command) || ghPRCreate.MatchString(command)
	}
	stripped, docs := stripHeredocs(command)
	piped := false
	for _, seg := range segments(stripped) {
		var body strings.Builder
		for _, doc := range docs {
			if doc.at >= seg.start && doc.at < seg.start+len(seg.text) {
				// An unquoted body runs its command substitutions, and
				// one whose end is unclear may hide a push.
				// Quotes in the body do not stop a substitution, so look
				// for a push in the raw text of any that has one.
				if doc.expands && (strings.Contains(doc.body, "$(") || strings.Contains(doc.body, "`")) && pipedPush.MatchString(doc.body) ||
					doc.unsure && (gitPush.MatchString(doc.body) || ghPRCreate.MatchString(doc.body)) {
					return true
				}
				body.WriteString(doc.body)
				body.WriteByte('\n')
			}
		}
		// Quoted text and a heredoc of data are skipped only for commands
		// known not to run their arguments; any other command is matched
		// on its whole text, as source /dev/stdin <<< 'git push' runs it.
		text := seg.text
		if inertCommand(seg.text) {
			text = maskQuoted(seg.text)
		} else if !runsCodeWords(seg.text) {
			text += "\n" + body.String()
		}
		if gitPush.MatchString(text) || ghPRCreate.MatchString(text) || quotedPush(seg.text) ||
			pushRuns(seg.text, body.String(), piped, command, depth) {
			return true
		}
		// A command that may run what a pipe carries (sed -n e, awk)
		// sees the line as the old text match did.
		if piped && !inertCommand(seg.text) && pipedPush.MatchString(command) {
			return true
		}
		// Once a pipe starts, later commands may share it, as in a
		// subshell: printf ... | (cd x; sh).
		piped = piped || seg.next == "|"
	}
	return false
}

// quotedPush reports whether a git or gh command pushes once the shell
// removes its quotes: git "push" origin HEAD, gh "pr" create. Masking
// quoted text would hide those words.
func quotedPush(text string) bool {
	name, args := commandName(text)
	switch name {
	case "git":
		i := 0
		for i < len(args) && strings.HasPrefix(args[i], "-") {
			if gitValueOptions[args[i]] {
				i++
			}
			i++
		}
		return i < len(args) && args[i] == "push"
	case "gh":
		return len(args) >= 2 && args[0] == "pr" && args[1] == "create"
	}
	return false
}

// inertCommands never run their arguments or stdin as code, so a push in
// their quoted arguments or heredoc is only text.
var inertCommands = map[string]bool{"git": true, "gh": true, "echo": true, "printf": true}

// commandName is a command's name, after keywords such as if and command and
// variable assignments, and the rest of its words.
func commandName(text string) (string, []string) {
	words := shellWords(text)
	for len(words) > 0 && (commandPrefixes[words[0]] || shellAssignment.MatchString(words[0])) {
		words = words[1:]
	}
	if len(words) == 0 || words[0] == "" || words[0] == "." {
		if len(words) > 0 {
			return words[0], words[1:]
		}
		return "", nil
	}
	return path.Base(filepath.ToSlash(words[0])), words[1:]
}

// inertCommand reports whether a command only reads its arguments as text
// or paths: git, gh, echo, printf, the search tools and readers, and sed
// with no script that runs a command.
func inertCommand(text string) bool {
	if words := shellWords(text); len(words) > 0 && shellAssignment.MatchString(words[0]) {
		// GIT_EDITOR='...' git commit runs the editor as shell code.
		return false
	}
	name, args := commandName(text)
	switch {
	case name == "git":
		// A -c setting can run shell code: an alias, core.editor,
		// core.sshCommand.
		return !slices.ContainsFunc(args, func(a string) bool {
			return a == "-c" || strings.HasPrefix(a, "-c") && !strings.HasPrefix(a, "--") || strings.HasPrefix(a, "!")
		})
	case inertCommands[name], searchCommands[name], readCommands[name]:
		return true
	case name == "sed":
		return !slices.ContainsFunc(args, sedExecute.MatchString) && len(optionValues(args, "f", "--file")) == 0
	}
	return false
}

// runsCodeWords reports whether a command is a shell, eval, an interpreter
// or a wrapper, whose code pushRuns reads.
func runsCodeWords(text string) bool {
	name, _ := commandName(text)
	return runsCode(name)
}

// pushRuns reports whether one command runs code that pushes: a shell's -c
// code, heredoc or eval arguments, a shell reading a pipe from a line that
// names a push, or interpreter code that spawns git push. line is the whole
// command line.
func pushRuns(text, body string, piped bool, line string, depth int) bool {
	if strs := hereStrings(text); len(strs) > 0 {
		body += strings.Join(strs, "\n") + "\n"
	}
	words := shellWords(text)
	for len(words) > 0 && (commandPrefixes[words[0]] || shellAssignment.MatchString(words[0])) {
		words = words[1:]
	}
	if len(words) == 0 || words[0] == "" {
		return false
	}
	if words[0] == "source" || words[0] == "." {
		// source runs a file, stdin included, as shell code.
		return pipedPush.MatchString(text+"\n"+body) || piped && pipedPush.MatchString(line)
	}
	if wrappers[path.Base(filepath.ToSlash(words[0]))] {
		// sudo -u me sh -c ..., env X=1 bash -c ...: the code runner is
		// further on, after the wrapper's options and their values.
		j := slices.IndexFunc(words, func(w string) bool {
			w = path.Base(filepath.ToSlash(w))
			return w == "eval" || shells[w] || interpreter(w)
		})
		if j < 0 {
			return false
		}
		words = words[j:]
	}
	name, args := path.Base(filepath.ToSlash(words[0])), words[1:]
	raw := pipedPush.MatchString
	switch {
	case name == "eval":
		return pushIn(strings.Join(args, " "), depth+1)
	case shells[name]:
		code, _, inline, _ := codeArgs(args, func(a string) bool {
			return strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "c")
		}, "oO", map[string]bool{"--rcfile": true, "--init-file": true})
		if inline {
			// The code may run its stdin: a pipe, or a here-string or
			// heredoc in body.
			return pushIn(code, depth+1) || piped && raw(line) || raw(body)
		}
		return pushIn(body, depth+1) || piped && raw(line)
	case interpreter(name):
		// The whole command, not only the -c value, so glued code such
		// as -c"..." counts too.
		code := text + "\n" + body
		// Code that runs a process may run what a pipe carries.
		return spawnsProcess.MatchString(code) && (interpreterPush.MatchString(code) || piped && raw(line))
	}
	return false
}

// pipedPush matches a push anywhere in a line whose text a shell reads from
// a pipe, quoted or not: echo "git push" | sh.
var pipedPush = regexp.MustCompile(`\bgit\b[^\n|]*\bpush\b|\bgh\b[^\n|]*\bpr\s+create\b`)

// spawnsProcess matches interpreter code that runs another program.
var spawnsProcess = regexp.MustCompile(`subprocess|os\.system|popen|Popen|spawn|child_process|execSync|execFile|\bexec[lv]?p?e?\b|\bsystem\b|Open3|IO\.popen|%x|qx|` + "`")

// interpreterPush matches git push in interpreter code, as a command string
// (global options such as -C dir included) or as a list of arguments, and
// gh pr create the same way.
var interpreterPush = regexp.MustCompile(`\bgit\b[^'"\n]*\bpush\b|['"]git['"](?:\s*,\s*['"][^'"\n]*['"])*?\s*,\s*['"]push['"]|\bgh\b[^'"\n]*\bpr\s+create\b|['"]gh['"]\s*,\s*['"]pr['"]\s*,\s*['"]create['"]`)

// maskQuoted blanks the inside of quoted strings, keeping the quotes, so a
// pattern matched over it sees only the words the shell runs and the shape
// of each quoted word.
func maskQuoted(text string) string {
	out := []byte(text)
	var quote byte
	for i := 0; i < len(out); i++ {
		c := out[i]
		switch {
		case quote != 0 && c == quote:
			quote = 0
		case quote == '"' && c == '\\' && i+1 < len(out):
			out[i], out[i+1] = ' ', ' '
			i++
		case quote != 0:
			if c != '\n' {
				out[i] = ' '
			}
		case c == '\\' && i+1 < len(out):
			i++
		case c == '\'' || c == '"':
			quote = c
		}
	}
	return string(out)
}

// overrideWord and acceptWord match the override subcommand and its --accept
// flag as shellWords gives them, which may run on into a shell operator, as in
// `--accept;`, and may keep the $ of an ANSI-C quote, as in $'--accept'.
var (
	overrideWord = regexp.MustCompile("^\\$?override(?:$|[;&|()<>`])")
	acceptWord   = regexp.MustCompile("^\\$?--accept(?:$|=|[;&|()<>`])")
)

// acceptsProposal reports whether a command accepts a proposed override. An
// agent proposes an override with --propose and a person accepts it; the
// hook refuses the agent's own --accept so that the decision stays theirs.
// Like forgedApproval, it guards against an agent's shortcut, not an attacker.
func acceptsProposal(command string) bool {
	// The shell drops a backslash-newline, joining the word around it. The
	// line is read whole, not split at separators that may sit in quotes: an
	// unrelated command beside it that names all three is blocked too.
	var slopcop, override, accept bool
	for _, word := range shellWords(strings.ReplaceAll(command, "\\\n", "")) {
		switch {
		case strings.ContainsAny(word, " \t\n"):
			// A quoted word with spaces is an argument value, such as a
			// reason, or a command for a shell (bash -c '...') judged alone.
			if acceptsProposal(word) {
				return true
			}
			if strings.Contains(strings.ToLower(word), "slopcop") {
				slopcop = true
			}
		case acceptWord.MatchString(word):
			accept = true
		case overrideWord.MatchString(word):
			override = true
		case strings.Contains(strings.ToLower(word), "slopcop"):
			// slopcop, ./bin/slopcop, slopcop.exe, brokk_slopcop
			slopcop = true
		}
	}
	return slopcop && override && accept
}

// acceptByAgentText is the reason an agent's `slopcop override --accept` is
// blocked.
const acceptByAgentText = "slopcop: blocked: only the user accepts a proposed override, so an agent may not run `slopcop override --accept`. Ask the user to run `slopcop override --accept` themselves (in Claude Code they type `! slopcop override --accept`), and wait for them"

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
	if p, ok := pendingProposal(root, tree); ok {
		return fmt.Errorf("slopcop: push blocked: HEAD's tree %s in %s has no passing slopcop check, and override %s proposed for it waits for the user (reason: %s). Ask the user to accept it by running `slopcop override --accept` in %s (in Claude Code: `! slopcop override --accept`); you cannot run it yourself. Then push", tree, root, p.ID, report.Printable(p.Reason), root)
	}
	return fmt.Errorf("slopcop: push blocked: HEAD's tree %s in %s has no passing slopcop check. The check approves exactly the content it saw: commit the checked state unchanged (or check the committed state), run `slopcop check` in %s until it approves the tree (see the slopcop skill), or for review findings that are wrong, or a static analyzer run that keeps failing, run `slopcop override --reason \"...\"` there, then push: as its own command, or joined to the push with && (`slopcop override --reason \"...\" && git push`). An agent that may not run it proposes one with `slopcop override --propose --reason \"...\"` for the user to accept", tree, root, root)
}

// pendingProposal returns the newest proposed override for tree that waits
// for a person, if any. An unreadable proposal log is read as none.
func pendingProposal(root, tree string) (review.Proposal, bool) {
	pending, _ := review.PendingProposals(root)
	for _, p := range pending {
		if p.Tree == tree {
			return p, true
		}
	}
	return review.Proposal{}, false
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
exact committed tree is reviewed. A shell command line that runs
slopcop override --reason "..." joined by && to the push passes when the latest
review is of HEAD's tree, since the override approves that tree before the
push runs. The hook reads its payload from stdin and blocks malformed input
rather than allowing an unchecked push.`,
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
				var input gateInput
				decoder := json.NewDecoder(io.LimitReader(cmd.InOrStdin(), (4<<20)+1))
				if err := decoder.Decode(&input); err != nil {
					return gateBlock(cmd, fmt.Errorf("slopcop: invalid %s hook input: %w", hook, err))
				}
				if input.CWD != "" {
					_ = cleanupInstalledLegacyRepository(legacyRepoRoot(input.CWD))
				}
				// Copilot CLI runs a repository's Claude Code settings too.
				// When its own gate is installed, that one judges the push;
				// otherwise this one does, as the payloads are alike.
				if hook == "claude" && underCopilotHook() && copilotHookInstalled(true) {
					return nil
				}
				shape, why := noOverride, ""
				switch {
				case shellTools[input.ToolName]:
					if forgedApproval(input.ToolInput.Command) {
						return gateBlock(cmd, errors.New(forgedApprovalText))
					}
					if acceptsProposal(input.ToolInput.Command) {
						return gateBlock(cmd, errors.New(acceptByAgentText))
					}
					if !pushCommand(input.ToolInput.Command) {
						return nil
					}
					shape, why = overrideBeforePush(input.ToolInput.Command)
					if shape == overrideOnly {
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
					err := gateTree(target)
					if err == nil {
						continue
					}
					switch shape {
					case overrideThenPush:
						// The override before the push approves the tree
						// it sends, if it succeeds; && stops it if not.
						notHead := overrideApprovesHead(input.CWD, target)
						if notHead == nil {
							continue
						}
						err = fmt.Errorf("%w. The `slopcop override` on this line would not approve it: %v", err, notHead)
					case overrideUncredited:
						err = fmt.Errorf("%w. The `slopcop override` on this line does not count: %s. %s", err, why, overrideOwnCommand)
					}
					return gateBlock(cmd, err)
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
