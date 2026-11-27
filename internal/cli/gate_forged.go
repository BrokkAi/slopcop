package cli

import (
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// Only slopcop check and slopcop override write SlopCop's approval refs, and
// neither is run with a ref on its command line, so a shell command that
// writes one is most likely an agent forging an approval the check did not
// give: git update-ref, or a file under .git/refs or packed-refs. Removing the
// state directory that arms the push gate would turn the gate off. Mentions
// of either are common in harmless commands, though: a grep over the docs, a
// pull request body, a commit message, a script that edits source.
//
// forgedApproval parses a command line that names the refs or the state
// directory (see parseShell) and reads it one simple command at a time, with
// heredoc bodies attached to the command that reads them, and blocks a command that could write, remove or
// read them. A command passes when it is one of the text tools below and the
// mention is text it prints, searches for or sends, not a file it opens or
// writes:
//
//   - echo and printf: every argument is text; gh: bodies and titles are.
//   - cat, head, ls and other readers, and filters such as tr and cut:
//     every argument is a path, so none may name the refs or a path under
//     .git.
//   - grep, rg, ag and git grep: the pattern is text; the rest are paths.
//   - sed: the script is text unless it names .git or update-ref, holds a
//     shell expansion or runs a command (e), since w file writes; the rest
//     are paths.
//   - git commit: the message is text; git log: the --grep, -S, -G, --author
//     and --committer values are. A global option such as -c core.editor
//     may not hold an expansion.
//   - bash -c, sh, eval and a heredoc fed to a shell: the code is read again
//     by these rules.
//   - python, node, ruby and perl code from -c, -e or a heredoc, not fed by
//     a pipe, unless it names update-ref, symbolic-ref, packed-refs,
//     --stdin, a .git path or a refspec into the refs, where it could write
//     a ref without git.
//
// Any other command that names them is blocked: git update-ref, branch, tag,
// symbolic-ref, for-each-ref and show-ref, rm, mv, cp, tee, find and the
// rest. So is any redirection to or from a path under .git, and, once the
// line names them anywhere, a command that runs update-ref or symbolic-ref,
// touches a .git path, or runs code it reads from elsewhere (a shell or
// interpreter fed on stdin or from a script file, xargs), since a pipe or a
// variable can carry the ref there. So is a word whose value the shell
// decides ($x, $(...), a glob or brace that may reach .git) where a path or
// code goes, a command fed by a pipe that is not one of the tools above (tar
// -T - reads paths from it), and a command run by a path outside the
// system's program directories, which may be a script the line wrote under a
// known tool's name (./cat). Reads of the state are blocked with the
// writes: an agent has no need to read approvals, and listing them is the
// first step to copying one. Like pushCommand, this guards against an
// agent's shortcut, not an attacker.
func forgedApproval(command string) bool {
	return forgedIn(command, false, false, 0)
}

// forgedApprovalText is the reason a command that may write the approval
// refs is blocked.
const forgedApprovalText = "slopcop: blocked: SlopCop owns refs/slopcop/ and .git/slopcop state; nothing else may write, read or remove them, and this command could. Mentioning them in text is fine (grep, echo, gh, git commit -m, a script that does not touch .git). Run `slopcop check` until it approves the tree, or for review findings that are wrong, or a static analyzer run that keeps failing, run `slopcop override --reason \"...\"`"

// approvalRef matches a mention of SlopCop's approval refs, as a ref name or
// as a path under .git, with either path separator.
var approvalRef = regexp.MustCompile(`refs[/\\]+slopcop\b`)

// reviewStatePath matches the repository-local state directory that arms the
// push gate. Removing it would turn the gate off without an approval.
var reviewStatePath = regexp.MustCompile(`\.git[/\\]+slopcop(?:[/\\]|\b)`)

// gitStatePath matches a path into a git directory or its packed-refs file,
// where a ref can be written without git update-ref.
var gitStatePath = regexp.MustCompile(`(?:^|[/\\=])\.git(?:[/\\]|$)|packed-refs`)

// refWriteHint matches code or arguments that write a ref or a file under
// .git: git's ref plumbing, packed-refs, a .git path in a string, or a
// refspec whose destination is an approval ref (git fetch . HEAD:refs/...),
// or --stdin, where git reads refs or refspecs from a pipe.
var refWriteHint = regexp.MustCompile(`update-ref|symbolic-ref|packed-refs|\.git(?:[/\\]|['"])|[:+]refs[/\\]+slopcop\b|--stdin\b`)

// codeVariables are the variables whose value Bash runs or looks up as
// commands: aliases, hashed commands, prompts and startup files.
var codeVariables = map[string]bool{
	"BASH_ALIASES": true, "BASH_CMDS": true, "PROMPT_COMMAND": true, "BASH_ENV": true, "ENV": true,
	"PS0": true, "PS1": true, "PS2": true, "PS3": true, "PS4": true,
}

// shellAssignment matches a variable assignment word before a command.
var shellAssignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// namesApproval reports whether text names the approval refs or the state
// directory.
func namesApproval(text string) bool {
	return approvalRef.MatchString(text) || reviewStatePath.MatchString(text)
}

// protectedPath reports whether a word, read as a path, names the approval
// refs, the state directory or anything under .git, or may expand to one: a
// variable or command substitution can carry a path another command on the
// line names, as in state=.git/slopcop; rm -rf "$state".
func protectedPath(word string) bool {
	return namesApproval(word) || gitStatePath.MatchString(word) || strings.ContainsAny(word, "$`")
}

// textCommands take only text arguments.
var textCommands = map[string]bool{"echo": true, "printf": true}

// readCommands take only paths and options, or text that is no path (tr's
// sets, jq's filter), and neither write a file nor read paths from stdin.
var readCommands = map[string]bool{
	"cat": true, "head": true, "tail": true, "wc": true, "less": true, "more": true, "sort": true,
	"uniq": true, "nl": true, "bat": true, "ls": true, "diff": true, "cmp": true, "file": true,
	"stat": true, "tree": true, "du": true, "tr": true, "cut": true, "column": true, "fold": true,
	"rev": true, "tac": true, "paste": true, "fmt": true, "expand": true, "unexpand": true, "jq": true,
}

// searchCommands take a pattern, then paths.
var searchCommands = map[string]bool{"grep": true, "egrep": true, "fgrep": true, "rg": true, "ag": true, "ack": true}

// shells run code from -c, a heredoc, stdin or a script file.
var shells = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "fish": true}

// interpreters run code from -c or -e, a heredoc, stdin or a script file.
var interpreters = map[string]bool{"python": true, "python2": true, "python3": true, "node": true, "ruby": true, "perl": true}

// interpreter reports whether a command name is an interpreter, versioned
// names such as python3.12 included.
func interpreter(name string) bool {
	if i := strings.IndexByte(name, '.'); i > 0 && strings.HasPrefix(name, "python") {
		name = name[:i]
	}
	return interpreters[name]
}

// runsCode reports whether a command runs code or commands it may read from
// elsewhere on the line, such as a pipe.
func runsCode(name string) bool {
	return shells[name] || interpreter(name) || name == "eval" || wrappers[name]
}

// wrappers run another command, which may read the ref from stdin or the
// line: xargs, env sh, sudo tee.
var wrappers = map[string]bool{
	"xargs": true, "source": true, ".": true, "env": true, "sudo": true, "doas": true, "su": true, "nohup": true, "exec": true,
	"nice": true, "timeout": true, "stdbuf": true, "watch": true, "parallel": true, "script": true,
	"flock": true, "chroot": true, "unbuffer": true, "busybox": true,
}

// forgedIn is forgedApproval for a command line, or for code a shell in one
// runs; named says the enclosing line names the refs or the state, and
// stdin that the code's stdin is a pipe or here-string.
func forgedIn(command string, named, stdin bool, depth int) bool {
	if !named && !namesApproval(command) {
		return false
	}
	if depth > 8 {
		return true
	}
	cmds, err := parseShell(command, stdin)
	if err != nil {
		// A line the parser cannot read may hide anything.
		return true
	}
	for _, cmd := range cmds {
		// Where the gate cannot be sure where a heredoc ends, its body may
		// hide a command.
		// The shell runs a command substitution in an unquoted heredoc as
		// it reads the body, and its output joins the body.
		if cmd.unsure || cmd.heredocSubst || forgedSegment(cmd, depth) {
			return true
		}
	}
	return false
}

// forgedSegment reports whether one command of a line that names the refs
// or the state could write, remove or read them.
func forgedSegment(cmd *shellCmd, depth int) bool {
	body := cmd.stdin()
	// A here-string is stdin, as a pipe is; a heredoc is judged as the
	// command's input or code.
	piped := cmd.pipe || len(cmd.hereStrings) > 0
	// A redirection writes or reads its file whatever the command is, and
	// $ref in one may carry the path.
	for i, file := range cmd.redirs {
		if cmd.redirDyn[i] || protectedPath(file) {
			return true
		}
	}
	if len(cmd.words) == 0 {
		// Assignments alone write nothing; a command that later reads
		// the variable is judged by its $. But an array element, or a
		// variable Bash runs as code, can hold a command no later $
		// shows: BASH_ALIASES[x]='rm -rf ...' defines an alias.
		for i, assign := range cmd.assigns {
			name, value, _ := strings.Cut(assign, "=")
			if (strings.Contains(name, "[") || codeVariables[name]) && (cmd.assignDyn[i] || namesApproval(value) || refWriteHint.MatchString(value) || protectedPath(value)) {
				return true
			}
		}
		return false
	}
	for i, assign := range cmd.assigns {
		if cmd.assignDyn[i] || protectedPath(assign[strings.IndexByte(assign, '=')+1:]) {
			// STATE=.git/slopcop cmd hands the command the path in its
			// environment.
			return true
		}
	}
	name := cmd.name()
	args := cmd.args()
	all := cmd.raw + "\n" + body
	// A command run by a path outside the system's program directories may
	// be a script the line wrote, even under a known tool's name (./cat).
	if cmd.byUnknownPath() {
		return true
	}
	// A command fed by a pipe may read paths from it: tar -T -, cpio. Only
	// the commands below are known not to.
	if !piped && !namesApproval(all) && !refWriteHint.MatchString(all) && !cmd.dyn[0] && !args.anyProtected(nil) &&
		!runsCode(name) && name != "sed" && !awkCommands[name] {
		return false
	}
	switch {
	case cmd.dyn[0]:
		// $cmd runs whatever the variable holds.
		return true
	case textCommands[name]:
		return false
	case name == "gh":
		// Bodies and titles are text; a path such as --body-file or
		// gh release download --output is not.
		return args.anyProtected(optionValues(args.vals, "bt", "--body", "--title", "--notes", "--subject"))
	case readCommands[name]:
		return args.anyProtected(nil)
	case searchCommands[name]:
		if slices.Contains(args.vals, "--files") {
			// rg --files lists files and takes no pattern.
			return args.anyProtected(nil)
		}
		return args.anyProtected(patternArgs(args.vals, "e", "--regexp"))
	case name == "sed":
		if len(optionValues(args.vals, "f", "--file")) > 0 {
			// A script from a file, or a pipe, is out of sight.
			return true
		}
		scripts := patternArgs(args.vals, "e", "--expression")
		for i := range scripts {
			// A script can write a file (w file), a shell variable in it
			// can carry the path, and e runs a command.
			if args.dyn[i] || refWriteHint.MatchString(args.vals[i]) || sedRuns(sedScript(args.vals[i])) {
				return true
			}
		}
		return args.anyProtected(scripts)
	case name == "git":
		return forgedGit(args)
	case awkCommands[name]:
		// The program is text unless it can run a command, write a file
		// or read one by name; the rest are paths.
		if len(optionValues(args.vals, "f", "--file")) > 0 {
			return true
		}
		program := -1
		for i := 0; i < len(args.vals); i++ {
			if !strings.HasPrefix(args.vals[i], "-") {
				program = i
				break
			}
			if takesOperand(args.vals[i], "Fv", nil) {
				i++
			}
		}
		if program >= 0 && (args.dyn[program] || awkRuns.MatchString(args.vals[program])) {
			return true
		}
		return args.anyProtected(map[int]bool{program: true})
	case name == "eval":
		return forgedIn(strings.Join(args.vals, " "), true, piped, depth+1)
	case shells[name]:
		code, rest, inline, script, _ := codeArgs(args, shellCodeOption, "oO", map[string]bool{"--rcfile": true, "--init-file": true}, false)
		if rest.anyProtected(nil) {
			return true
		}
		if inline {
			// A heredoc or here-string is the code's stdin, not code.
			return forgedIn(code, true, piped || body != "", depth+1)
		}
		if script || len(cmd.heredocs)+len(cmd.hereStrings) == 0 || cmd.heredocExpands {
			// Code from a pipe or a script file is out of sight; with a
			// script, a heredoc is only its input. The shell expands an
			// unquoted heredoc before the code runs, so $c in it may be
			// any code.
			return true
		}
		for _, code := range append(slices.Clone(cmd.heredocs), cmd.hereStrings...) {
			if forgedIn(code, true, piped, depth+1) {
				return true
			}
		}
		return false
	case interpreter(name):
		code, rest, inline, script, codeDyn := codeArgs(args, interpreterCodeOption(name), interpreterValueShort(name), map[string]bool{"--require": true, "--import": true}, true)
		if !inline && (script || cmd.heredocExpands) || inline && (piped || codeDyn) {
			// A script file is out of sight, inline code may run or
			// open what the pipe carries, and "$code" or an expansion in
			// a heredoc of code may be anything.
			return true
		}
		code += "\n" + body
		if strings.TrimSpace(code) == "" || refWriteHint.MatchString(code) {
			return true
		}
		return rest.anyProtected(nil)
	}
	return true
}

// shellCodeOption reports whether a shell option gives code as the next
// word: -c, or a cluster with c such as -lc.
func shellCodeOption(arg string) bool {
	return strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.Contains(arg, "c")
}

// interpreterCodeOption is the test for an interpreter's options that give
// code as the next word.
func interpreterCodeOption(name string) func(string) bool {
	return func(arg string) bool {
		return arg == "-c" || arg == "-e" || arg == "-E" && name == "perl" || arg == "--eval"
	}
}

// interpreterValueShort is an interpreter's short options that take the
// next word as their value: python -W and -X, node and ruby -r. Perl's take
// theirs glued.
func interpreterValueShort(name string) string {
	switch {
	case strings.HasPrefix(name, "python"):
		return "WX"
	case name == "node", name == "ruby":
		return "r"
	}
	return ""
}

// awkCommands are the awk implementations.
var awkCommands = map[string]bool{"awk": true, "gawk": true, "mawk": true, "nawk": true}

// awkRuns matches an awk program that may run a command (system, a pipe),
// write a file (>) or read one by name (getline, ARGV).
var awkRuns = regexp.MustCompile(`system|getline|[|>]|ENVIRON|ARGV|ARGC|close|fflush`)

// gitValueOptions are git's global options that take the next word as their
// value.
var gitValueOptions = map[string]bool{"-C": true, "-c": true, "--git-dir": true, "--work-tree": true, "--namespace": true, "--config-env": true}

// forgedGit reports whether a git command could write or read the refs:
// anything but a commit message, a log search or a git grep pattern. A
// global option's value may carry the path, and a -c setting such as
// core.editor runs shell code.
func forgedGit(args argv) bool {
	i := 0
	for i < len(args.vals) && strings.HasPrefix(args.vals[i], "-") {
		if gitValueOptions[args.vals[i]] {
			i++
		}
		i++
	}
	if i >= len(args.vals) || args.sub(0, i).anyProtected(nil) {
		return true
	}
	sub, rest := args.vals[i], args.sub(i+1, len(args.vals))
	switch sub {
	case "commit":
		return rest.anyProtected(optionValues(rest.vals, "m", "--message"))
	case "log":
		return rest.anyProtected(optionValues(rest.vals, "SG", "--grep", "--author", "--committer"))
	case "grep":
		return rest.anyProtected(patternArgs(rest.vals, "e", "--regexp"))
	}
	return true
}

// optionValues finds the arguments that are values of the given options:
// a short option letter in short, alone or in a cluster, with its value
// glued or next, or a long option as --opt value or --opt=value. A glued
// value marks its whole word.
func optionValues(args []string, short string, long ...string) map[int]bool {
	values := map[int]bool{}
	// next marks the word after i, if there is one: an option that ends
	// the line has no value.
	next := func(i int) {
		if i+1 < len(args) {
			values[i+1] = true
		}
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			return values
		case strings.HasPrefix(arg, "--"):
			for _, opt := range long {
				if arg == opt {
					next(i)
					i++
					break
				}
				if strings.HasPrefix(arg, opt+"=") {
					values[i] = true
					break
				}
			}
		case len(arg) > 1 && arg[0] == '-':
			// A cluster is option letters; anything else, such as the
			// path of -O.git/x, is the value of the letter before it. A
			// value letter is first, its value glued, or last, its value
			// next: in -Ointernal/x the t belongs to O's value.
			letters := arg[1:]
			if end := strings.IndexFunc(letters, func(r rune) bool { return !unicode.IsLetter(r) }); end >= 0 {
				letters = letters[:end]
			}
			if k := strings.IndexAny(letters, short); k == 0 || k >= 0 && k+2 == len(arg) {
				if k+2 == len(arg) {
					next(i)
					i++
				} else {
					values[i] = true
				}
			}
		}
	}
	return values
}

// patternArgs finds a search tool's or sed's patterns: the -e (or long)
// values, or else the first operand, unless the patterns come from a file
// (-f), when every operand is a path.
func patternArgs(args []string, short string, long string) map[int]bool {
	values := optionValues(args, short, long)
	if len(values) > 0 || len(optionValues(args, "f", "--file")) > 0 {
		return values
	}
	for i := 0; i < len(args); i++ {
		if args[i] == "" {
			// The suffix of macOS sed -i ''.
			continue
		}
		if args[i] == "--" {
			if i+1 < len(args) {
				values[i+1] = true
			}
			break
		}
		if !strings.HasPrefix(args[i], "-") || args[i] == "-" {
			values[i] = true
			break
		}
		if takesOperand(args[i], searchValueShort, searchValueLong) {
			i++
		}
	}
	return values
}

// searchValueShort and searchValueLong are the options of grep, rg, ag and
// sed that take the next word as their value, such as rg -g '*.go', so it is
// not read as the pattern.
const searchValueShort = "gtTmABCMjdD"

var searchValueLong = map[string]bool{
	"--glob": true, "--iglob": true, "--type": true, "--type-not": true, "--max-count": true,
	"--context": true, "--after-context": true, "--before-context": true, "--include": true,
	"--exclude": true, "--exclude-dir": true, "--max-depth": true, "--max-filesize": true,
	"--threads": true, "--ignore-file": true, "--exclude-from": true, "--include-from": true, "--pre": true, "--pre-glob": true, "--color": true, "--colors": true, "--sort": true, "--encoding": true,
}

// takesOperand reports whether an option takes the next word as its value:
// a long option in long given without =, or a cluster of letters whose last
// letter is in short.
func takesOperand(arg, short string, long map[string]bool) bool {
	if strings.HasPrefix(arg, "--") {
		return long[arg]
	}
	letters := arg[1:]
	return letters != "" && strings.IndexFunc(letters, func(r rune) bool { return !unicode.IsLetter(r) }) < 0 &&
		strings.ContainsRune(short, rune(letters[len(letters)-1]))
}

// codeArgs splits an interpreter's arguments into the code an option such
// as -c gives and every other argument: options, a script file and its
// arguments; whether an option gave code; and, where none did, whether a
// script file runs (the first operand that is not an option's value). After
// an interpreter's -, code comes from stdin and the rest are its arguments. codeDyn says the
// code holds an expansion, so it is not known before the shell runs.
func codeArgs(args argv, codeOption func(string) bool, valueShort string, valueLong map[string]bool, dashIsStdin bool) (code string, rest argv, inline, script, codeDyn bool) {
	for i := 0; i < len(args.vals); i++ {
		arg := args.vals[i]
		if codeOption(arg) && i+1 < len(args.vals) {
			rest := argv{vals: append(slices.Clone(args.vals[:i]), args.vals[i+2:]...), dyn: append(slices.Clone(args.dyn[:i]), args.dyn[i+2:]...)}
			return args.vals[i+1], rest, true, false, args.dyn[i+1]
		}
		if arg == "-" {
			if dashIsStdin {
				// python3 - arg: code from stdin, as from a heredoc,
				// and the rest are its arguments.
				return "", args, false, false, false
			}
			// A shell's - ends its options: the next operand is a
			// script.
			continue
		}
		if !strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "+") {
			return "", args, false, true, false
		}
		if takesOperand(strings.Replace(arg, "+", "-", 1), valueShort, valueLong) {
			i++
		}
	}
	return "", args, false, false, false
}
