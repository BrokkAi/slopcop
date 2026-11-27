package cli

import (
	"path"
	"path/filepath"
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
// forgedApproval reads a command line that names the refs or the state
// directory one command at a time, with heredoc bodies attached to the
// command that reads them, and blocks a command that could write, remove or
// read them. A command passes when it is one of the text tools below and the
// mention is text it prints, searches for or sends, not a file it opens or
// writes:
//
//   - echo and printf: every argument is text; gh: bodies and titles are.
//   - cat, head, ls and other readers: every argument is a path, so none may
//     name the refs or a path under .git.
//   - grep, rg, ag and git grep: the pattern is text; the rest are paths.
//   - sed: the script is text unless it names .git or update-ref, holds a
//     shell variable or runs a command (e), since w file writes; the rest
//     are paths.
//   - git commit: the message is text; git log: the --grep, -S, -G, --author
//     and --committer values are.
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
// variable can carry the ref there. Reads of the state are blocked with the
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

// sedVariable matches a shell variable or substitution left in a sed
// script, as in sed "w $ref", but not sed's own $ for the last line.
var sedVariable = regexp.MustCompile("\\$[A-Za-z_{(]|`")

// sedExecute matches GNU sed's e command, or the e flag of s with any
// delimiter (s/x/y/e, s|x|y|e, sX^XXe), which run a line or the pattern
// space as a shell command. It errs towards matching: any e that ends a
// command counts.
var sedExecute = regexp.MustCompile(`(?:^|[;{}\n/\d$!])\s*e(?:\s|$|;|})|\Se[gpiImM0-9w]*\s*(?:$|;|})|[^\w\s\\]\s+e[gpiImM0-9w]*\s*(?:$|;|})`)

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

// readCommands take only paths and options.
var readCommands = map[string]bool{
	"cat": true, "head": true, "tail": true, "wc": true, "less": true, "more": true, "sort": true,
	"uniq": true, "nl": true, "bat": true, "ls": true, "diff": true, "cmp": true, "file": true,
	"stat": true, "tree": true, "du": true,
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
	stripped, docs := stripHeredocs(command)
	// The first command reads the stdin of the code's shell, which may be
	// a pipe.
	piped := stdin
	for _, seg := range segments(stripped) {
		var body strings.Builder
		count := 0
		for _, doc := range docs {
			if doc.at >= seg.start && doc.at < seg.start+len(seg.text) {
				count++
				// Where the gate cannot be sure where a heredoc ends,
				// or which one a command reads, its body may hide a
				// command; and the shell runs a command substitution in
				// an unquoted heredoc as it reads the body.
				if doc.unsure || count > 1 ||
					doc.expands && (strings.Contains(doc.body, "$(") || strings.Contains(doc.body, "`")) {
					return true
				}
				body.WriteString(doc.body)
				body.WriteByte('\n')
			}
		}
		if forgedSegment(seg.text, body.String(), piped, depth) {
			return true
		}
		// A subshell's ( leaves an empty command that keeps the pipe.
		// Once a pipe starts, later commands may share it, as in a
		// subshell: printf ... | (cd x; python3 -c ...).
		piped = piped || seg.next == "|"
	}
	return false
}

// forgedSegment reports whether one command of a line that names the refs
// or the state could write, remove or read them. body holds the heredocs the
// command reads; piped says its stdin comes from the command before it.
func forgedSegment(text, body string, piped bool, depth int) bool {
	if strs := hereStrings(text); len(strs) > 0 {
		// A here-string is stdin, as a pipe or heredoc is.
		piped = true
		body += strings.Join(strs, "\n") + "\n"
	}
	// A redirection writes or reads its file whatever the command is, and
	// $ref in one may carry the path.
	if slices.ContainsFunc(redirectFiles(text), protectedPath) {
		return true
	}
	words := shellWords(text)
	assigned := false
	for len(words) > 0 && (commandPrefixes[words[0]] || shellAssignment.MatchString(words[0])) {
		if shellAssignment.MatchString(words[0]) && protectedPath(words[0][strings.IndexByte(words[0], '=')+1:]) {
			assigned = true
		}
		words = words[1:]
	}
	if len(words) == 0 {
		// Assignments alone write nothing; a command that later reads
		// the variable is judged by its $.
		return false
	}
	if assigned {
		// STATE=.git/slopcop cmd hands the command the path in its
		// environment.
		return true
	}
	name := words[0]
	if name != "" && name != "." {
		// path.Base would turn "" into ".", the source builtin.
		name = path.Base(filepath.ToSlash(name))
	}
	all := text + "\n" + body
	// A command run by its path (./x, /tmp/x) may be a script the line
	// wrote, so it is classified too: a known tool by its base name, any
	// other file blocked.
	byPath := strings.ContainsAny(words[0], `/\`)
	if !namesApproval(all) && !refWriteHint.MatchString(all) && !slices.ContainsFunc(words, protectedPath) && !runsCode(name) && name != "sed" && !awkCommands[name] && !byPath {
		return false
	}
	args := words[1:]
	switch {
	case textCommands[name]:
		return false
	case name == "gh":
		// Bodies and titles are text; a path such as --body-file or
		// gh release download --output is not.
		return pathsProtected(args, optionValues(args, "bt", "--body", "--title", "--notes", "--subject"))
	case readCommands[name]:
		return slices.ContainsFunc(args, protectedPath)
	case searchCommands[name]:
		if slices.Contains(args, "--files") {
			// rg --files lists files and takes no pattern.
			return slices.ContainsFunc(args, protectedPath)
		}
		return pathsProtected(args, patternArgs(args, "e", "--regexp"))
	case name == "sed":
		if len(optionValues(args, "f", "--file")) > 0 {
			// A script from a file, or a pipe, is out of sight.
			return true
		}
		scripts := patternArgs(args, "e", "--expression")
		for i := range scripts {
			// A script can write a file (w file), and a variable in it
			// can carry the path.
			if refWriteHint.MatchString(args[i]) || sedVariable.MatchString(args[i]) || sedExecute.MatchString(args[i]) {
				return true
			}
		}
		return pathsProtected(args, scripts)
	case name == "git":
		return forgedGit(args)
	case awkCommands[name]:
		// The program is text unless it can run a command, write a file
		// or read one by name; the rest are paths.
		if len(optionValues(args, "f", "--file")) > 0 {
			return true
		}
		program := -1
		for i := 0; i < len(args); i++ {
			if !strings.HasPrefix(args[i], "-") {
				program = i
				break
			}
			if takesOperand(args[i], "Fv", nil) {
				i++
			}
		}
		if program >= 0 && awkRuns.MatchString(args[program]) {
			return true
		}
		return pathsProtected(args, map[int]bool{program: true})
	case name == "eval":
		return forgedIn(strings.Join(args, " "), true, piped, depth+1)
	case shells[name]:
		code, rest, inline, script := codeArgs(args, func(a string) bool {
			return strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "c")
		}, "oO", map[string]bool{"--rcfile": true, "--init-file": true})
		if slices.ContainsFunc(rest, protectedPath) || !inline && (body == "" || script) {
			// Code from stdin or a script file is out of sight; with
			// a script, a heredoc is only its input.
			return true
		}
		return forgedIn(code+"\n"+body, true, piped, depth+1)
	case interpreter(name):
		code, rest, inline, script := codeArgs(args, func(a string) bool {
			return a == "-c" || a == "-e" || a == "-E" && name == "perl" || a == "--eval"
		}, "WXr", map[string]bool{"--require": true, "--import": true})
		if !inline && script || inline && piped {
			// A script file is out of sight, and inline code may run
			// or open what the pipe carries.
			return true
		}
		code += "\n" + body
		if strings.TrimSpace(code) == "" || refWriteHint.MatchString(code) {
			return true
		}
		return slices.ContainsFunc(rest, protectedPath)
	}
	return true
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
// anything but a commit message, a log search or a git grep pattern.
func forgedGit(args []string) bool {
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		if gitValueOptions[args[i]] {
			i++
		}
		i++
	}
	if i >= len(args) || slices.ContainsFunc(args[:i], namesApproval) {
		return true
	}
	sub, rest := args[i], args[i+1:]
	switch sub {
	case "commit":
		return pathsProtected(rest, optionValues(rest, "m", "--message"))
	case "log":
		return pathsProtected(rest, optionValues(rest, "SG", "--grep", "--author", "--committer"))
	case "grep":
		return pathsProtected(rest, patternArgs(rest, "e", "--regexp"))
	}
	return true
}

// pathsProtected reports whether any argument but the text ones is a
// protected path.
func pathsProtected(args []string, text map[int]bool) bool {
	for i, arg := range args {
		if !text[i] && protectedPath(arg) {
			return true
		}
	}
	return false
}

// optionValues finds the arguments that are values of the given options:
// a short option letter in short, alone or in a cluster, with its value
// glued or next, or a long option as --opt value or --opt=value. A glued
// value marks its whole word.
func optionValues(args []string, short string, long ...string) map[int]bool {
	values := map[int]bool{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			return values
		case strings.HasPrefix(arg, "--"):
			for _, opt := range long {
				if arg == opt {
					values[i+1] = true
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
					values[i+1] = true
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
// script file runs (the first operand that is not an option's value or -).
func codeArgs(args []string, codeOption func(string) bool, valueShort string, valueLong map[string]bool) (code string, rest []string, inline, script bool) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if codeOption(arg) && i+1 < len(args) {
			return args[i+1], append(slices.Clone(args[:i]), args[i+2:]...), true, false
		}
		if arg == "-" {
			// Code from stdin, as from a heredoc.
			continue
		}
		if !strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "+") {
			return "", args, false, true
		}
		if takesOperand(strings.Replace(arg, "+", "-", 1), valueShort, valueLong) {
			i++
		}
	}
	return "", args, false, false
}

// hereStrings is the text of a command's here-strings (<<< word), which the
// command reads on stdin.
func hereStrings(text string) []string {
	var strs []string
	var quote byte
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else if c == '\\' && quote == '"' {
				i++
			}
		case c == '\\':
			i++
		case c == '\'' || c == '"':
			quote = c
		case strings.HasPrefix(text[i:], "<<<"):
			if words := shellWords(text[i+3:]); len(words) > 0 {
				strs = append(strs, words[0])
			}
			i += 2
		}
	}
	return strs
}

// redirectFiles is the files a command's redirections read or write. A
// heredoc's delimiter and a here-string's text are not files, nor is a
// descriptor such as the 1 of 2>&1.
func redirectFiles(text string) []string {
	var files []string
	var quote byte
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else if c == '\\' && quote == '"' {
				i++
			}
			continue
		case c == '\\':
			i++
			continue
		case c == '\'' || c == '"':
			quote = c
			continue
		case c != '<' && c != '>':
			continue
		}
		j := i
		for j < len(text) && strings.IndexByte("<>&|", text[j]) >= 0 {
			j++
		}
		op := text[i:j]
		i = j - 1
		if strings.HasPrefix(op, "<<") {
			continue
		}
		rest := strings.TrimLeft(text[j:], " \t")
		if strings.HasSuffix(op, "&") && rest != "" && (rest[0] >= '0' && rest[0] <= '9' || rest[0] == '-') {
			continue
		}
		if words := shellWords(rest); len(words) > 0 {
			files = append(files, words[0])
		}
	}
	return files
}

// heredoc is a heredoc's body and where its operator stands in the command
// line with the bodies taken out.
type heredoc struct {
	at   int
	body string
	// expands says the delimiter is unquoted, so the shell expands
	// variables and runs command substitutions in the body.
	expands bool
	// unsure says the gate cannot be sure where the body ends: the
	// delimiter is not a plain word, quoted whole or not at all, the body
	// has no delimiter line, or an unquoted body continues a line with a
	// backslash.
	unsure bool
}

// plainDelimiter matches the heredoc delimiters the gate reads with
// confidence: a word, quoted whole or not at all.
var plainDelimiter = regexp.MustCompile(`^(?:[A-Za-z_][\w.-]*|'[A-Za-z_][\w.-]*'|"[A-Za-z_][\w.-]*"|\\[A-Za-z_][\w.-]*)$`)

// heredocOperator matches a heredoc operator and its delimiter word, quoted
// in whole or in part or not at all, at the start of the text.
var heredocOperator = regexp.MustCompile(`^<<(-?)[ \t]*((?:'[^'\n]*'|"[^"\n]*"|\\.|[^\s;&|()<>'"\\])+)`)

// stripHeredocs takes the heredoc bodies out of a command line, so that text
// in them is neither split into commands nor read as quotes. It follows
// quotes and command substitution as segments does, so a << in quoted text
// starts no heredoc, and one in "$(cat <<'EOF' ...)" does.
func stripHeredocs(command string) (string, []heredoc) {
	type pending struct {
		at      int
		delim   string
		tabs    bool
		expands bool
		plain   bool
	}
	var out strings.Builder
	var docs []heredoc
	var open []pending
	var quote byte
	var parens []byte
	// escaped is where the last escaped character stood: an escaped blank
	// does not end a word, so a # after it starts no comment.
	escaped := -1
	for i := 0; i < len(command); i++ {
		c := command[i]
		if c == '\n' && len(open) > 0 && quote == 0 {
			out.WriteByte(c)
			next := i + 1
			for _, doc := range open {
				var body string
				var ended bool
				body, next, ended = heredocBody(command, next, doc.delim, doc.tabs)
				continued := doc.expands && strings.Contains(body, "\\\n")
				docs = append(docs, heredoc{at: doc.at, body: body, expands: doc.expands,
					unsure: !doc.plain || !ended || continued})
			}
			open = nil
			i = next - 1
			continue
		}
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
		case quote == '"':
			switch {
			case c == '\\' && i+1 < len(command):
				out.WriteByte(c)
				i++
				c = command[i]
			case c == '"':
				quote = 0
			case c == '$' && i+1 < len(command) && command[i+1] == '(':
				out.WriteByte(c)
				i++
				c = command[i]
				parens = append(parens, '"')
				quote = 0
			}
		case c == '\\' && i+1 < len(command):
			out.WriteByte(c)
			i++
			c = command[i]
			escaped = i
		case c == '#' && commentStart(command, i, escaped):
			// A comment runs to the end of the line, and a quote or a
			// << in it is not one.
			end := strings.IndexByte(command[i:], '\n')
			if end < 0 {
				i = len(command)
			} else {
				i += end - 1
			}
			continue
		case c == '\'' || c == '"':
			quote = c
		case c == '(':
			parens = append(parens, 0)
		case c == ')':
			if n := len(parens); n > 0 {
				quote, parens = parens[n-1], parens[:n-1]
			}
		case c == '<' && (i == 0 || command[i-1] != '<') && !inArithmetic(out.String()):
			if m := heredocOperator.FindStringSubmatch(command[i:]); m != nil {
				delim := ""
				if words := shellWords(m[2]); len(words) > 0 {
					delim = words[0]
				}
				open = append(open, pending{at: out.Len(), delim: delim, tabs: m[1] == "-",
					expands: !strings.ContainsAny(m[2], `'"\\`), plain: plainDelimiter.MatchString(m[2])})
				out.WriteString(m[0])
				i += len(m[0]) - 1
				continue
			}
		}
		out.WriteByte(c)
	}
	for _, doc := range open {
		docs = append(docs, heredoc{at: doc.at, unsure: true})
	}
	return out.String(), docs
}

// inArithmetic reports whether the text so far is inside an
// arithmetic expansion or command, $((...)), ((...)) or $[...], where << is a
// shift, not a heredoc.
func inArithmetic(text string) bool {
	return strings.Count(text, "((") > strings.Count(text, "))") ||
		strings.LastIndex(text, "$[") > strings.LastIndex(text, "]")
}

// commentStart reports whether the # at i starts a comment: it starts a word,
// after a blank or an operator that was not escaped. A backslash-newline
// joins the lines, so the character before it is the one that counts.
func commentStart(command string, i, escaped int) bool {
	k := i - 1
	for k >= 1 && command[k] == '\n' && command[k-1] == '\\' {
		k -= 2
	}
	return k < 0 || k != escaped && strings.IndexByte(" \t\n;&|(", command[k]) >= 0
}

// heredocBody reads a heredoc body from start up to its delimiter line, or
// the end of the command, and returns the body and where the command
// resumes, and whether it found the delimiter. With tabs, as for <<-, leading tabs do not count on the
// delimiter line.
func heredocBody(command string, start int, delim string, tabs bool) (string, int, bool) {
	for pos := start; pos < len(command); {
		lineEnd, next := len(command), len(command)
		if end := strings.IndexByte(command[pos:], '\n'); end >= 0 {
			lineEnd, next = pos+end, pos+end+1
		}
		line := command[pos:lineEnd]
		if tabs {
			line = strings.TrimLeft(line, "\t")
		}
		if line == delim {
			return command[start:pos], next, true
		}
		pos = next
	}
	return command[start:], len(command), false
}
