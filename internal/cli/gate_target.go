package cli

import (
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// The hook payload's cwd is the agent session's working directory, which is
// not always where a push runs: `cd ../other && git push` and
// `git -C ../other push` push another repository. pushTargets follows a
// command line's cd, git -C and --git-dir to the repositories its pushes may
// run in, so the gate judges the trees that would be pushed rather than the
// session's.
//
// Where the shell could run a push in more than one directory, as around ||,
// every one is judged. A cd in a pipeline, a background job or a subshell
// does not move the commands after it. Where a directory that matters cannot
// be read without running the shell ($VAR, $(...), cd -, popd), or does not
// exist yet because the same line makes it (git worktree add, cp -r), the
// gate cannot tell what would be pushed and blocks, asking for a push it can
// read, when it guards a repository in sight: the session's, a push's, or one
// the line names (the source of a cp -r). It reads the line with the same
// parser as pushCommand. Like pushCommand, this guards against an agent's
// slip; it is not a security boundary.

// unknownDir stands for a directory that cannot be read from the command
// line. A path never resolves to it.
const unknownDir = ""

// segment is one command of a command line and the operator after it, ""
// at the end.
type segment struct {
	text string
	next string
}

// segments splits a command line at the operators that end a command: &&,
// ||, ;, &, |, newlines, subshells and command substitution. Quoted text and
// escaped characters are not operators, and a backslash-newline is
// removed, as the shell removes it: pu\<newline>sh is push. The & of a
// redirection (2>&1, >&file, &>file) is not an operator, and |& is a pipe;
// shellWords reads the redirections themselves.
func segments(command string) []segment {
	command = strings.ReplaceAll(command, "\\\n", "")
	var parts []segment
	start := 0
	var quote byte
	// parens and backticks hold, for each open ( and `, the quote to return
	// to when it closes: a command substitution inside double quotes runs
	// commands as one outside them does.
	var parens, backticks []byte
	emit := func(i int, op string) {
		parts = append(parts, segment{text: command[start:i], next: op})
		start = i + len(op)
	}
	for i := 0; i < len(command); i++ {
		c := command[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
			continue
		case quote == '"':
			switch {
			case c == '\\':
				i++
			case c == '"':
				quote = 0
			case c == '$' && i+1 < len(command) && command[i+1] == '(':
				i++
				emit(i, "(")
				parens = append(parens, '"')
				quote = 0
			case c == '`':
				emit(i, "`")
				backticks = append(backticks, '"')
				quote = 0
			}
			continue
		case c == '\\':
			i++
			continue
		case c == '\'' || c == '"':
			quote = c
			continue
		}
		redirected := i > 0 && (command[i-1] == '>' || command[i-1] == '<')
		switch {
		case (c == '&' || c == '|') && redirected:
			// >&, <& and >| belong to a redirection.
			continue
		case c == '&' && i+1 < len(command) && command[i+1] == '>':
			// &> and &>> redirect both outputs to a file.
			continue
		case c == '|' && i+1 < len(command) && command[i+1] == '&':
			// |& is a pipe that carries stderr too.
			emit(i, "|")
			start = i + 2
			i++
			continue
		}
		op := ""
		switch {
		case strings.HasPrefix(command[i:], "&&"), strings.HasPrefix(command[i:], "||"):
			op = command[i : i+2]
		case c == '(':
			op = "("
			parens = append(parens, 0)
		case c == ')':
			op = ")"
			if n := len(parens); n > 0 {
				quote, parens = parens[n-1], parens[:n-1]
			}
		case c == '`':
			op = "`"
			if n := len(backticks); n > 0 {
				quote, backticks = backticks[n-1], backticks[:n-1]
			} else {
				backticks = append(backticks, 0)
			}
		case strings.IndexByte(";&|\n", c) >= 0:
			op = command[i : i+1]
		default:
			continue
		}
		emit(i, op)
		i += len(op) - 1
	}
	return append(parts, segment{text: command[start:]})
}

// backslashSeparates says a backslash is a path separator unless it escapes
// a blank, a quote or a shell operator character, as in a Windows path such
// as C:\Users\me\repo. It is a variable so tests can read both forms.
var backslashSeparates = runtime.GOOS == "windows"

// shellEscapable are the characters a backslash escapes even where it
// otherwise separates path parts.
const shellEscapable = " \t\"'\\$`;&|()<>"

// shellWords splits a command into words as the shell does before
// expansion: quotes and backslashes group and are removed, so `my\ repo` and
// "my repo" are one word. On Windows a backslash before any other character
// stays, as a path separator. Redirections are dropped with their files and
// descriptors, glued or not: `cd dir>/dev/null 2>&1` is the words cd, dir.
func shellWords(text string) []string {
	var words []string
	var word strings.Builder
	inWord := false
	// skipWord drops the next word: a redirection's file.
	skipWord := false
	flush := func() {
		if inWord {
			if !skipWord {
				words = append(words, word.String())
			}
			skipWord = false
		}
		word.Reset()
		inWord = false
	}
	var quote byte
	for i := 0; i < len(text); i++ {
		c := text[i]
		if quote == 0 && (c == '<' || c == '>' || c == '&' && i+1 < len(text) && text[i+1] == '>') {
			// A word of digits just before is the descriptor redirected.
			if inWord && !skipWord && isDigits(word.String()) {
				word.Reset()
				inWord = false
			}
			flush()
			j := i + 1
			for j < len(text) && strings.IndexByte("<>&|", text[j]) >= 0 {
				j++
			}
			op := text[i:j]
			if strings.HasSuffix(op, "&") {
				// >&2 and >&- copy or close a descriptor and name no file.
				k := j
				for k < len(text) && (text[k] >= '0' && text[k] <= '9' || text[k] == '-') {
					k++
				}
				if k > j {
					i = k - 1
					continue
				}
			}
			skipWord = true
			i = j - 1
			continue
		}
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			} else {
				word.WriteByte(c)
			}
		case quote == '"':
			switch {
			case c == '\\' && i+1 < len(text) && strings.IndexByte("\"\\$`", text[i+1]) >= 0:
				i++
				word.WriteByte(text[i])
			case c == '"':
				quote = 0
			default:
				word.WriteByte(c)
			}
		case c == '\\' && i+1 < len(text):
			if !backslashSeparates || strings.IndexByte(shellEscapable, text[i+1]) >= 0 {
				i++
			}
			word.WriteByte(text[i])
			inWord = true
		case c == '$' && i+1 < len(text) && text[i+1] == '\'':
			// $'...' is a quote that decodes backslash escapes: git $'push'.
			i = ansiQuoted(text, i+2, &word)
			inWord = true
		case c == '$' && i+1 < len(text) && text[i+1] == '"':
			// $"..." is a translated double quote.
			i++
			quote = '"'
			inWord = true
		case c == '\'' || c == '"':
			quote = c
			inWord = true
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			flush()
		default:
			word.WriteByte(c)
			inWord = true
		}
	}
	flush()
	return words
}

// ansiQuoted decodes the body of a $'...' quote that starts at i into word,
// as the parser does for the gate's checks, and returns where its closing
// quote stands.
func ansiQuoted(text string, i int, word *strings.Builder) int {
	start := i
	for ; i < len(text) && text[i] != '\''; i++ {
		if text[i] == '\\' {
			i++
		}
	}
	end := min(i, len(text))
	quoted := "$'" + text[start:end] + "'"
	file, err := shellParser().Parse(strings.NewReader(quoted), "")
	if err == nil && len(file.Stmts) == 1 {
		if call, ok := file.Stmts[0].Cmd.(*syntax.CallExpr); ok && len(call.Args) == 1 {
			if v, err := expand.Literal(nil, call.Args[0]); err == nil {
				word.WriteString(v)
				return i
			}
		}
	}
	word.WriteString(text[start:end])
	return i
}

// isDigits reports whether s is one or more ASCII digits.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// resolveDir is where a cd or git -C argument points from dir, or unknownDir
// when it cannot be read without running the shell. A relative argument from
// an unknown directory is unknown too.
func resolveDir(dir, arg string) string {
	for _, home := range []string{"$HOME", "${HOME}"} {
		if arg == home || strings.HasPrefix(arg, home+"/") {
			arg = "~" + strings.TrimPrefix(arg, home)
		}
	}
	if arg == "" || arg == "-" || strings.ContainsAny(arg, "$`*?[") {
		return unknownDir
	}
	if arg == "~" || strings.HasPrefix(arg, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return unknownDir
		}
		arg = filepath.Join(home, strings.TrimPrefix(arg, "~"))
	} else if strings.HasPrefix(arg, "~") {
		return unknownDir
	}
	if !filepath.IsAbs(arg) {
		if dir == unknownDir {
			return unknownDir
		}
		arg = filepath.Join(dir, arg)
	}
	return filepath.Clean(arg)
}

// commandPrefixes are words that can stand before a command in the same
// shell: keywords of compound commands and the builtins that run another.
var commandPrefixes = map[string]bool{
	"builtin": true, "command": true, "{": true, "!": true, "time": true,
	"if": true, "then": true, "else": true, "elif": true, "do": true, "while": true, "until": true,
}

// cdTarget is the directory a cd, pushd or popd command moves to, and
// whether the segment is one. The words come from shellWords, so
// redirections are already gone. The target is unknownDir where it cannot be
// read.
func cdTarget(dir string, words []string) (string, bool) {
	for len(words) > 0 && commandPrefixes[words[0]] {
		words = words[1:]
	}
	if len(words) == 0 {
		return "", false
	}
	switch words[0] {
	case "cd", "pushd":
	case "popd":
		return unknownDir, true
	default:
		return "", false
	}
	args := words[1:]
	for len(args) > 0 && (args[0] == "-L" || args[0] == "-P" || args[0] == "-e" || args[0] == "--") {
		args = args[1:]
	}
	switch len(args) {
	case 0:
		home, err := os.UserHomeDir()
		if err != nil {
			return unknownDir, true
		}
		return home, true
	case 1:
		return resolveDir(dir, args[0]), true
	default:
		return unknownDir, true
	}
}

// gitDir is the repository a git command pushes: dir moved by each -C, in
// order, as git applies them, then the one --git-dir (or GIT_DIR set before
// the command) names, whose refs and HEAD the push reads. --work-tree does
// not change what is pushed. It is unknownDir when an argument cannot be
// read.
func gitDir(dir string, words []string) string {
	for i := 0; i < len(words); i++ {
		if filepath.Base(words[i]) != "git" {
			continue
		}
		// git reads --git-dir from the directory -C leaves it in, and the
		// flag wins over the environment.
		named := ""
		for _, word := range words[:i] {
			if value, ok := strings.CutPrefix(word, "GIT_DIR="); ok {
				named = value
			}
		}
		for j := i + 1; j < len(words); j++ {
			word := words[j]
			switch {
			case word == "push":
				if named != "" {
					return checkoutOfGitDir(dir, named)
				}
				return dir
			case word == "-C" && j+1 < len(words):
				if dir = resolveDir(dir, words[j+1]); dir == unknownDir {
					return unknownDir
				}
				j++
			case word == "--git-dir" && j+1 < len(words):
				named = words[j+1]
				j++
			case strings.HasPrefix(word, "--git-dir="):
				named = strings.TrimPrefix(word, "--git-dir=")
			case word == "-c" || word == "--work-tree" || word == "--namespace" || word == "--config-env":
				j++
			case !strings.HasPrefix(word, "-"):
				// Another subcommand: this git is not a push.
				j = len(words)
			}
		}
	}
	return dir
}

// checkoutOfGitDir is the checkout a --git-dir value names from dir: a
// checkout's .git names its checkout, and any other git directory itself.
func checkoutOfGitDir(dir, value string) string {
	path := resolveDir(dir, value)
	if path != unknownDir && filepath.Base(path) == ".git" {
		return filepath.Dir(path)
	}
	return path
}

// Why pushTargets could not read every directory a push may run in.
const (
	// unsureShell is a directory that depends on the shell.
	unsureShell = "shell"
	// unsureMissing is a directory that does not exist yet.
	unsureMissing = "missing"
	// unsureLimit is a line too long or deep for the reader to follow
	// within its bounds; the gate blocks it wherever it runs.
	unsureLimit = "limit"
)

// pushTargets lists the directories the pushes on a command line may run in,
// starting from cwd, and why some push may run in a directory it cannot read
// ("" when it read them all). Where the shell could run a push in more than
// one directory, it lists each: after `cd dir && make || <push>`, the push
// runs in the old directory when the cd failed and in dir when make did.
//
// It reads the line's syntax tree (see parseShell) and follows a small part
// of the shell exactly: lists joined by ;, & and newlines, && and ||,
// pipelines, subshells, { } blocks, if, !, command substitutions, cd, pushd
// and popd, and the pushes git and gh make. Each statement maps the
// directories it may start in to those it may end in: where the next command
// runs when it succeeded, as after &&, and anywhere it may have stopped, as
// after ||. A pipeline, a subshell, a background job and a substitution keep
// their cd to themselves. In a list joined by ; or a newline, a cd alone into
// a directory that exists is taken to succeed.
//
// What it does not follow, it does not guess at: a line that defines a
// function or an alias or sets a trap, eval and source (which may move the
// shell anywhere), code a shell or interpreter runs that names a push or
// that it reads from a pipe, a script the same line may have written, a loop
// or case that moves the shell, and CDPATH make the target unsure, and the
// gate then asks for a push it can read.
func pushTargets(cwd, command string) ([]string, string) {
	e := evalTargets(cwd, command)
	return e.targets, e.unsure
}

// evalTargets reads a command line from cwd for pushTargets and visitedDirs.
func evalTargets(cwd, command string) *targetEval {
	e := &targetEval{seen: map[string]bool{}, writes: writesFiles(command) || pushText(command), cdpath: os.Getenv("CDPATH") != "", funcs: map[string][]funcBody{}}
	e.exports = slices.ContainsFunc(startupVars, func(v string) bool { return os.Getenv(v) != "" })
	start := []string{cwd}
	if !e.read(command, start) {
		// A line the parser cannot read may push where this cannot see.
		e.add(start)
		e.unsure = unsureShell
	}
	if e.unsure != "" && len(e.targets) == 0 {
		e.add(start)
	}
	if e.limited {
		e.unsure = unsureLimit
	}
	return e
}

// read reads shell code run from dirs: the line itself, or code a shell
// in it runs. It returns whether the parser read it whole; the statements
// before a syntax error, which the shell runs, are read either way. A
// function the code defines makes the target unsure, and its body is read
// too, from anywhere the code went, for the repositories it may push.
func (e *targetEval) read(src string, dirs []string) bool {
	ok, _ := e.readFlow(src, dirs)
	return ok
}

// readFlow is read, also returning where the code may end.
func (e *targetEval) readFlow(src string, dirs []string) (bool, flow) {
	file, err := shellParser().Parse(strings.NewReader(src), "")
	var stmts []*syntax.Stmt
	if err == nil {
		stmts = file.Stmts
	} else {
		for stmt, stmtErr := range shellParser().StmtsSeq(strings.NewReader(src)) {
			if stmtErr != nil {
				break
			}
			stmts = append(stmts, stmt)
		}
	}
	outer, bodies, writes := e.c, e.bodies, e.writes
	e.c, e.bodies = &cmdCollector{src: src}, nil
	// Code a shell runs may write the script it then runs.
	e.writes = e.writes || writesFiles(src) || pushText(src)
	for _, stmt := range stmts {
		syntax.Walk(stmt, func(n syntax.Node) bool {
			switch n := n.(type) {
			case *syntax.FuncDecl:
				// Any later command may be a call that moves the shell
				// or pushes. Its body is still read where it is called,
				// for the repositories it may push.
				e.unsure = unsureShell
				if n.Name != nil {
					e.funcs[n.Name.Value] = append(e.funcs[n.Name.Value], funcBody{c: e.c, body: n.Body})
				}
			case *syntax.DeclClause:
				switch n.Variant.Value {
				case "export", "declare", "typeset":
					e.exports = true
				}
			case *syntax.CallExpr:
				if len(n.Args) > 0 {
					if name, _ := e.c.word(n.Args[0]); name == "set" || name == "exec" && len(n.Args) == 1 {
						// set -a, set -o allexport: every assignment
						// after it exports. exec 3< <(...) opens a
						// descriptor every later command inherits.
						e.exports = true
					}
				}
			case *syntax.Redirect:
				// A heredoc or here-string may be code, or a script the
				// line writes and runs.
				if n.Op == syntax.Hdoc || n.Op == syntax.DashHdoc || n.Op == syntax.WordHdoc {
					e.bodies = append(e.bodies, e.c.stdinBody(n))
				}
			}
			return true
		})
	}
	f := e.stmts(stmts, dirs)
	if e.writes && e.runner && slices.ContainsFunc(e.bodies, namesPush) {
		// A heredoc that names a push may be written to a file that a
		// command not known to read only text then runs (awk
		// '{system($0)}' file, make -f file). Its bodies are read for
		// the repositories it may push.
		e.unsure = unsureShell
		e.readCode(e.bodies, union(dirs, f.any))
	}
	// A function may also be called where this cannot see (find -exec
	// bash -c f), so every body the code defines is read once more from
	// anywhere the code went.
	for _, stmt := range stmts {
		syntax.Walk(stmt, func(n syntax.Node) bool {
			fd, ok := n.(*syntax.FuncDecl)
			if ok && e.depth >= 8 {
				// A body not read may push anywhere.
				e.limited = true
			} else if ok {
				// One pass a definition, its calls not followed, so it
				// costs no more than the line's length.
				flat := e.flat
				e.flat = true
				e.depth++
				e.stmt(fd.Body, union(dirs, f.any))
				e.depth--
				e.flat = flat
			}
			return true
		})
	}
	e.c, e.bodies, e.writes = outer, bodies, writes
	return err == nil, f
}

// visitedDirs lists the directories a command line's commands may run in,
// shell code it runs included, as far as the reader can tell.
func visitedDirs(cwd, command string) []string {
	return evalTargets(cwd, command).visited
}

// funcBody is a function's body and the collector of the code it is in.
type funcBody struct {
	c    *cmdCollector
	body *syntax.Stmt
}

// shellMoves is the builtins that move the shell or set its state, and run
// no file.
var shellMoves = map[string]bool{"cd": true, "pushd": true, "popd": true, "true": true, ":": true, "false": true, "export": true, "unset": true, "set": true}

// maxFlowDirs bounds the directories a statement may leave the shell in,
// maxCodeReads the pieces of code one line's best-effort reading reads, and
// maxSteps the statements its reading reads in all.
const (
	maxFlowDirs  = 64
	maxCodeReads = 256
	maxSteps     = 20000
)

// maxFunctionCalls bounds the function bodies one line's reading follows.
const maxFunctionCalls = 64

// targetEval gathers the directories pushes run in.
type targetEval struct {
	// funcs are the bodies the line defines for each function name, and
	// calls how many have been read where they were called.
	funcs map[string][]funcBody
	calls int
	// flat says a function body is being read without following the
	// calls in it.
	flat bool
	// feed is the text the command being read gets on stdin from a pipe or
	// a compound command, and feedUnknown says it is a file this cannot
	// read.
	feed        string
	feedUnknown bool
	// limited says the reader hit one of its bounds (function calls,
	// depth, code read) and may have lost where a push runs.
	limited bool
	// exports is whether the line may export a variable: export, declare,
	// typeset, set -a.
	exports bool
	// moves keeps whether a loop or case may move the shell.
	moves map[syntax.Node]bool
	// runner is whether a command read may run what a file holds: one
	// not known to read only text or to move the shell.
	runner bool
	// cur is the statement being read, whose output a process
	// substitution >(...) in it reads.
	cur *syntax.Stmt
	// reads counts the pieces of code read on a best-effort basis, and
	// steps the statements read.
	reads, steps int
	// placed counts the pushes given directories, new ones or not.
	placed  int
	c       *cmdCollector
	targets []string
	seen    map[string]bool
	unsure  string
	// cdpath says a relative cd may follow CDPATH.
	cdpath bool
	// writes says the line may write a file, such as a script it then
	// runs.
	writes bool
	// piped says the command being read gets a pipe on stdin, or the
	// stdin of a compound command around it (a heredoc, a here-string, a
	// file).
	piped bool
	// bodies are the line's heredocs and here-strings.
	bodies []string
	// visited is every directory a command may run in.
	visited []string
	// depth is how deep in shell code the reader is.
	depth int
}

// flow is where commands may run after a statement: succ where the next one
// runs when it succeeded, any wherever it may have stopped, and after where
// the next one runs in a list joined by ; or a newline.
type flow struct {
	succ, any, after []string
}

// still is the flow of a statement that moves no one.
func still(dirs []string) flow {
	return flow{succ: dirs, any: dirs, after: dirs}
}

// anywhere is the flow of a statement that may leave the shell in any
// directory.
func anywhere(dirs []string) flow {
	moved := union(dirs, []string{unknownDir})
	return flow{succ: moved, any: moved, after: moved}
}

// add records the directories a push may run in, in a stable order.
func (e *targetEval) add(dirs []string) {
	e.placed++
	dirs = slices.Clone(dirs)
	slices.Sort(dirs)
	for _, target := range dirs {
		switch {
		case target == unknownDir:
			e.unsure = unsureShell
		case !isDir(target):
			// A directory missing now may be one the same command makes
			// (git worktree add, cp -r, git clone), which the gate cannot
			// judge before it exists.
			if e.unsure == "" {
				e.unsure = unsureMissing
			}
		case !e.seen[target]:
			e.seen[target] = true
			e.targets = append(e.targets, target)
		}
	}
}

// stmts reads a list of statements joined by ;, & or newlines.
func (e *targetEval) stmts(list []*syntax.Stmt, dirs []string) flow {
	cur, all := dirs, dirs
	for _, s := range list {
		if e.steps > maxSteps {
			// Past the bound: the rest is not read (e.stmt sets limited).
			break
		}
		f := e.stmt(s, cur)
		all = union(all, f.any)
		if !s.Background {
			cur = f.after
		}
	}
	all = union(all, cur)
	return flow{succ: cur, any: all, after: cur}
}

// stmt reads one statement run from dirs.
func (e *targetEval) stmt(s *syntax.Stmt, dirs []string) flow {
	e.steps++
	if e.steps > maxSteps {
		// Too much to read: too complex to follow.
		e.limited = true
		return anywhere(dirs)
	}
	e.visited = union(e.visited, dirs)
	outer := e.cur
	e.cur = s
	defer func() { e.cur = outer }()
	// Command substitutions in redirections and heredocs run first, in a
	// subshell.
	for _, r := range s.Redirs {
		e.substs(r.Word, dirs)
		if r.Hdoc != nil {
			e.substs(r.Hdoc, dirs)
		}
	}
	piped, feed, unknown := e.piped, e.feed, e.feedUnknown
	// cmd < <(cat <<EOF ...) or cmd <(cat <<EOF ...): what the process
	// substitution writes is read as through a pipe.
	var reads []*syntax.Word
	for _, r := range s.Redirs {
		if (r.Op == syntax.RdrIn || r.Op == syntax.RdrInOut) && (r.N == nil || r.N.Value == "0") {
			reads = append(reads, r.Word)
		}
	}
	if call, ok := s.Cmd.(*syntax.CallExpr); ok {
		reads = append(reads, call.Args...)
	}
	for _, w := range reads {
		for _, part := range w.Parts {
			if ps, ok := part.(*syntax.ProcSubst); ok && ps.Op == syntax.CmdIn {
				var b strings.Builder
				for _, st := range ps.Stmts {
					b.WriteString(e.stmtText(st) + "\n" + e.heredocsOf(st) + "\n")
				}
				e.piped, e.feed = true, b.String()+e.feed
			}
		}
	}
	if _, isCall := s.Cmd.(*syntax.CallExpr); !isCall {
		for _, r := range s.Redirs {
			if r.N != nil && r.N.Value != "0" {
				continue
			}
			switch r.Op {
			case syntax.Hdoc, syntax.DashHdoc, syntax.WordHdoc:
				// { cd x; bash; } <<< ...: the commands inside read it.
				e.piped, e.feed, e.feedUnknown = true, e.c.stdinBody(r), false
			case syntax.RdrIn, syntax.RdrInOut, syntax.DplIn:
				// A file: what it holds is unknown.
				e.piped, e.feed, e.feedUnknown = true, "", true
			}
		}
	}
	var f flow
	if s.Background {
		// A background job runs in a subshell.
		e.sub(func() { f = e.command(s, dirs) })
	} else {
		f = e.command(s, dirs)
	}
	e.piped, e.feed, e.feedUnknown = piped, feed, unknown
	if len(f.any) > maxFlowDirs {
		// Branches that each move the shell multiply where it may be:
		// too complex to follow.
		e.limited = true
		f = anywhere(dirs)
	}
	e.visited = union(e.visited, f.any)
	if setsCDPATH(e.c, s) {
		// From here on, a relative cd may follow CDPATH.
		e.cdpath = true
	}
	switch {
	case s.Background:
		// A background job runs in a subshell.
		return still(dirs)
	case s.Negated:
		// ! cmd succeeds where cmd failed, and leaves the shell where cmd
		// did.
		return flow{succ: f.any, any: f.any, after: f.after}
	}
	return f
}

// command reads the command of a statement.
func (e *targetEval) command(s *syntax.Stmt, dirs []string) flow {
	switch x := s.Cmd.(type) {
	case *syntax.CallExpr:
		return e.call(s, x, dirs)
	case *syntax.BinaryCmd:
		switch x.Op {
		case syntax.AndStmt:
			a := e.stmt(x.X, dirs)
			b := e.stmt(x.Y, a.succ)
			any := union(a.any, b.any)
			return flow{succ: b.succ, any: any, after: any}
		case syntax.OrStmt:
			a := e.stmt(x.X, dirs)
			b := e.stmt(x.Y, a.any)
			any := union(a.any, b.any)
			return flow{succ: union(a.succ, b.succ), any: any, after: any}
		default:
			// A pipeline runs each command in a subshell, and the right
			// one reads the left one's output.
			e.sub(func() { e.stmt(x.X, dirs) })
			piped, feed, unknown := e.piped, e.feed, e.feedUnknown
			// The right side reads what the left side prints: its text,
			// heredocs included, is what it may run.
			// What the left side reads may pass through it too (cat).
			e.piped, e.feed, e.feedUnknown = true, e.stmtText(x.X)+"\n"+e.heredocsOf(x.X)+"\n"+feed, unknown
			e.sub(func() { e.stmt(x.Y, dirs) })
			e.piped, e.feed, e.feedUnknown = piped, feed, unknown
			return still(dirs)
		}
	case *syntax.Subshell:
		e.sub(func() { e.stmts(x.Stmts, dirs) })
		return still(dirs)
	case *syntax.Block:
		return e.stmts(x.Stmts, dirs)
	case *syntax.IfClause:
		return e.ifClause(x, dirs)
	case *syntax.TimeClause:
		if x.Stmt != nil {
			return e.stmt(x.Stmt, dirs)
		}
	case *syntax.CoprocClause:
		// A coprocess runs in a subshell.
		e.sub(func() { e.stmt(x.Stmt, dirs) })
	case *syntax.WhileClause, *syntax.ForClause, *syntax.CaseClause:
		// A loop or case is read once, from wherever it may begin; one
		// that may move the shell may end anywhere.
		inner := dirs
		if e.movesShell(x) {
			inner = union(dirs, []string{unknownDir})
		}
		// Each body is read as a list, so a cd in it carries to what
		// follows.
		end := inner
		switch x := x.(type) {
		case *syntax.WhileClause:
			cond := e.stmts(x.Cond, inner)
			end = union(cond.any, e.stmts(x.Do, cond.any).any)
		case *syntax.ForClause:
			e.substs(x.Loop, inner)
			end = union(inner, e.stmts(x.Do, inner).any)
		case *syntax.CaseClause:
			e.substs(x.Word, inner)
			// After ;& or ;;&, the next branch may run too, from where
			// this one ended.
			var fell []string
			for _, item := range x.Items {
				for _, p := range item.Patterns {
					e.substs(p, union(inner, fell))
				}
				f := e.stmts(item.Stmts, union(inner, fell))
				end = union(end, f.any)
				fell = nil
				if item.Op != syntax.Break {
					fell = f.any
				}
			}
		}
		if e.movesShell(x) {
			// It may end anywhere, the directories its body went to
			// among them.
			return anywhere(end)
		}
	case nil:
	case *syntax.FuncDecl:
		// A definition runs nothing; its body is read where it is
		// called, and once at the end.
	default:
		// [[ ]], (( )), let, declare: only their substitutions run.
		e.substs(x, dirs)
	}
	return still(dirs)
}

// ifClause reads if, elif and else: the branch taken runs after its
// condition, and with no else the if may end where its condition did.
func (e *targetEval) ifClause(x *syntax.IfClause, dirs []string) flow {
	if len(x.Cond) == 0 {
		// else
		return e.stmts(x.Then, dirs)
	}
	cond := e.stmts(x.Cond, dirs)
	then := e.stmts(x.Then, cond.succ)
	other := still(cond.any)
	if x.Else != nil {
		other = e.ifClause(x.Else, cond.any)
	}
	any := union(cond.any, union(then.any, other.any))
	return flow{succ: union(then.succ, other.succ), any: any, after: union(then.after, other.after)}
}

// callFunc reads a call of a function the line defines (already unsure):
// each body is read here, in this shell, for where it may go. It may also
// run none of them.
func (e *targetEval) callFunc(bodies []funcBody, dirs []string) flow {
	result := still(dirs)
	if e.depth >= 8 || e.flat {
		// A call not followed may go anywhere.
		e.limited = true
		return anywhere(dirs)
	}
	// Past the budget, a body is still read, but the calls in it are not
	// followed: each call then costs no more than the line's length.
	flat := e.calls+len(bodies) > maxFunctionCalls
	e.limited = e.limited || flat
	e.depth++
	e.flat = flat
	for _, f := range bodies {
		e.calls++
		outer := e.c
		e.c = f.c
		r := e.stmt(f.body, dirs)
		e.c = outer
		result = flow{succ: union(result.succ, r.succ), any: union(result.any, r.any), after: union(result.after, r.after)}
	}
	e.flat = false
	e.depth--
	return result
}

// substs reads the command and process substitutions in a node, which run in
// subshells.
func (e *targetEval) substs(n syntax.Node, dirs []string) {
	e.substsIn(n, dirs, false)
}

// substsIn is substs, quoted saying n is inside double quotes.
func (e *targetEval) substsIn(n syntax.Node, dirs []string, quoted bool) {
	syntax.Walk(n, func(m syntax.Node) bool {
		switch m := m.(type) {
		case *syntax.DblQuoted:
			// "${x:-<(...)}": text, but "$(...)" still runs.
			for _, part := range m.Parts {
				e.substsIn(part, dirs, true)
			}
			return false
		case *syntax.CmdSubst:
			e.sub(func() { e.stmts(m.Stmts, dirs) })
			return false
		case *syntax.ParamExp:
			if quoted || m.Exp == nil || m.Exp.Word == nil {
				return true
			}
			// ${x:-<(...)}: the parser keeps a process substitution in a
			// default as text, though the shell runs it. It is read as
			// code for where it may push; the answer is unsure.
			// Quoted, it is text: ${x:-'<(...)'}.
			if !slices.ContainsFunc(m.Exp.Word.Parts, func(p syntax.WordPart) bool {
				lit, ok := p.(*syntax.Lit)
				return ok && (strings.Contains(lit.Value, "<(") || strings.Contains(lit.Value, ">("))
			}) {
				return true
			}
			from, to := int(m.Exp.Word.Pos().Offset()), int(m.Exp.Word.End().Offset())
			if to > len(e.c.src) || from > to {
				return true
			}
			e.add(dirs)
			e.unsure = unsureShell
			e.readCode([]string{": " + e.c.src[from:to]}, dirs)
			return true
		case *syntax.ProcSubst:
			if m.Op == syntax.CmdOut && e.cur != nil {
				// cmd > >(awk ...): the consumer reads what the
				// statement writes, its heredocs or a file's content.
				piped, feed, unknown := e.piped, e.feed, e.feedUnknown
				e.piped, e.feed, e.feedUnknown = true, e.stmtText(e.cur)+"\n"+e.heredocsOf(e.cur), true
				e.sub(func() { e.stmts(m.Stmts, dirs) })
				e.piped, e.feed, e.feedUnknown = piped, feed, unknown
				return false
			}
			e.sub(func() { e.stmts(m.Stmts, dirs) })
			return false
		}
		return true
	})
}

// sub runs fn as a subshell: CDPATH set in it does not reach the shell.
func (e *targetEval) sub(fn func()) {
	cdpath := e.cdpath
	fn()
	e.cdpath = cdpath
}

// movesShell reports whether a loop or case holds a command that may move the
// shell it runs in: cd, pushd, popd, or one this does not follow.
func (e *targetEval) movesShell(n syntax.Node) bool {
	if moves, ok := e.moves[n]; ok {
		return moves
	}
	found := false
	syntax.Walk(n, func(m syntax.Node) bool {
		switch m := m.(type) {
		case *syntax.WhileClause, *syntax.ForClause, *syntax.CaseClause:
			if m != n {
				// Judged once, and kept, so nested loops cost their
				// size once.
				found = found || e.movesShell(m)
				return false
			}
		case *syntax.Subshell, *syntax.CmdSubst, *syntax.ProcSubst, *syntax.CoprocClause:
			return false
		case *syntax.Stmt:
			// A background job runs in a subshell.
			return !m.Background
		case *syntax.BinaryCmd:
			// A pipeline runs each command in a subshell.
			return m.Op != syntax.Pipe && m.Op != syntax.PipeAll
		case *syntax.CallExpr:
			if len(m.Args) > 0 {
				name, dyn := e.c.word(m.Args[0])
				switch name {
				case "cd", "pushd", "popd", "command", "builtin", "eval", "source", ".":
					found = true
				}
				// A function the line defines may move it too.
				found = found || dyn || len(e.funcs[name]) > 0
			}
		}
		return !found
	})
	if e.moves == nil {
		e.moves = map[syntax.Node]bool{}
	}
	e.moves[n] = found
	return found
}

// inShellCode are the builtins that run code in the shell itself, which may
// move it anywhere or set what later commands do.
var inShellCode = map[string]bool{"eval": true, "source": true, ".": true, "trap": true, "alias": true, "enable": true}

// call reads a simple command: a cd that moves the commands after it, a push,
// and code it runs that this does not follow.
func (e *targetEval) call(s *syntax.Stmt, x *syntax.CallExpr, dirs []string) flow {
	for _, a := range x.Assigns {
		e.substs(a, dirs)
	}
	for _, w := range x.Args {
		e.substs(w, dirs)
	}
	// Pushes in the substitutions are placed; the rest is judged alone.
	before := e.placed
	if len(x.Args) > 0 {
		// A function named command or builtin runs before the prefix
		// it shadows would be taken off.
		if first, _ := e.c.word(x.Args[0]); (first == "command" || first == "builtin") && len(e.funcs[first]) > 0 {
			return e.callFunc(e.funcs[first], dirs)
		}
	}
	cmd, _ := e.c.simple(s, false)
	if cmd == nil || len(cmd.words) == 0 {
		return still(dirs)
	}
	if cmd.dyn[0] {
		// $cmd runs whatever the variable holds: p='git push'; $p. It
		// may be any function the line defines, whose bodies are read
		// here for the repositories they may push.
		e.add(dirs)
		e.unsure = unsureShell
		var all []funcBody
		for _, bodies := range e.funcs {
			all = append(all, bodies...)
		}
		r := e.callFunc(all, dirs)
		return anywhere(union(r.any, r.after))
	}
	name := cmd.name()
	if !inertCommand(cmd) && !shellMoves[name] {
		// A command that may run what a file holds (awk, make, a
		// script).
		e.runner = true
	}
	if bodies := e.funcs[name]; len(bodies) > 0 && !cmd.bypass {
		return e.callFunc(bodies, dirs)
	}
	if inShellCode[name] && !strings.ContainsAny(cmd.words[0], `/\`) {
		// eval, source, trap: code in this shell this does not follow.
		// It is still read for the repositories it may push: eval's and
		// trap's code, an alias's value, and the line's heredocs and
		// here-strings (source /dev/stdin <<< ...).
		e.unsure = unsureShell
		codes := slices.Clone(e.bodies)
		switch name {
		case "eval":
			codes = append(codes, strings.Join(cmd.args().vals, " "))
		case "trap":
			// trap [-lp] [--] code signal...
			for _, arg := range cmd.args().vals {
				if !strings.HasPrefix(arg, "-") {
					codes = append(codes, arg)
					break
				}
			}
		case "alias":
			for _, word := range cmd.args().vals {
				if _, value, ok := strings.Cut(word, "="); ok {
					codes = append(codes, value)
				}
			}
		}
		// It may end anywhere, where that code goes among them.
		return anywhere(union(dirs, e.readCode(codes, dirs)))
	}
	// CDPATH=x cd sets it for the cd; CDPATH= cd clears it.
	cdpath := e.cdpath
	for _, a := range cmd.assigns {
		if value, ok := strings.CutPrefix(a, "CDPATH="); ok {
			cdpath = value != ""
		}
	}
	if f, ok := cdFlow(dirs, cmd.words, cdpath); ok {
		return f
	}
	words := append(slices.Clone(cmd.assigns), cmd.words...)
	// Behind a wrapper, the program's own words: env git -C x "push".
	// An option's value cannot be told from the program (env -u git git
	// push), so every git or gh word after a wrapper counts.
	gitPushes, ghPushes := pushWords(name, cmd.args().vals) && name == "git", pushWords(name, cmd.args().vals) && name == "gh"
	if wrappers[name] {
		for j := 1; j < len(cmd.words); j++ {
			if w := path.Base(filepath.ToSlash(cmd.words[j])); pushWords(w, cmd.words[j+1:]) {
				gitPushes = gitPushes || w == "git"
				ghPushes = ghPushes || w == "gh"
			}
		}
	}
	switch {
	case gitPush.MatchString(cmd.masked) || gitPushes:
		var repos []string
		for _, dir := range dirs {
			repos = union(repos, []string{gitDir(dir, words)})
			if wrappers[name] {
				// Each git word may be the program, as an option's value
				// may read git too (env -u git git -C x push).
				for j := 1; j < len(cmd.words); j++ {
					if path.Base(filepath.ToSlash(cmd.words[j])) == "git" {
						repos = union(repos, []string{gitDir(dir, cmd.words[j:])})
					}
				}
			}
		}
		if wrappers[name] {
			// A wrapper may run the program somewhere else (env -C,
			// sudo -i, su -), which this does not follow.
			repos = append(repos, unknownDir)
		}
		e.add(repos)
	case ghPRCreate.MatchString(cmd.masked) || ghPushes:
		if wrappers[name] {
			e.add([]string{unknownDir})
		}
		e.add(dirs)
	}
	// A command fed by a pipe or a compound command's stdin may run
	// what it reads (awk system, sed e) when the line names a push, as
	// pushCommand reads it.
	// What it reads names a push, and it is no command known to read
	// only text (make -f -, awk system, sed e).
	pipedRunner := e.piped && !inertCommand(cmd) && (namesPush(e.feed) || e.feedUnknown && pipedPush.MatchString(e.c.src))
	if pipedRunner || e.runsUnfollowedCode(s, cmd, dirs) {
		e.add(dirs)
		e.unsure = unsureShell
		// The answer is unsure; the code is still read for the
		// repositories it may push, so a guarded one is in sight: its -c
		// code, and every heredoc and here-string on the line (its stdin,
		// or a script the line writes).
		var codes []string
		for _, run := range shellRuns(cmd) {
			if run.code != "" {
				codes = append(codes, run.code)
			}
		}
		e.readCode(append(codes, e.bodies...), dirs)
	} else if e.placed == before && pushIn(e.stmtText(s), 1) {
		// A push the push check sees here that this has not placed, as
		// in find -exec git push or awk '{system($0)}' <<EOF: it may run
		// anywhere. The line's heredocs are read for where it may go.
		e.add(dirs)
		e.unsure = unsureShell
		e.readCode(e.bodies, dirs)
	}
	return still(dirs)
}

// heredocsOf is the bodies of the heredocs and here-strings in a statement.
func (e *targetEval) heredocsOf(s *syntax.Stmt) string {
	var b strings.Builder
	syntax.Walk(s, func(n syntax.Node) bool {
		if r, ok := n.(*syntax.Redirect); ok && (r.Op == syntax.Hdoc || r.Op == syntax.DashHdoc || r.Op == syntax.WordHdoc) {
			b.WriteString(e.c.stdinBody(r))
			b.WriteByte('\n')
		}
		return true
	})
	return b.String()
}

// stmtText is a statement's source, substitutions shortened, with each
// heredoc body the statement's span leaves out (cat <<EOF; x) appended
// after it, with its delimiter line.
func (e *targetEval) stmtText(s *syntax.Stmt) string {
	text := textWithoutSubsts(e.c, s)
	for _, r := range s.Redirs {
		if r.Hdoc != nil && len(r.Hdoc.Parts) > 0 && r.Hdoc.Pos().Offset() >= s.End().Offset() {
			text += "\n" + spanWithoutSubsts(e.c, r.Hdoc, int(r.Hdoc.Parts[0].Pos().Offset()), int(r.Hdoc.End().Offset()))
		}
	}
	return text
}

// textWithoutSubsts is a statement's source with each command and process
// substitution shortened to $(): a push in one is read on its own.
func textWithoutSubsts(c *cmdCollector, s *syntax.Stmt) string {
	return spanWithoutSubsts(c, s, int(s.Pos().Offset()), int(s.End().Offset()))
}

// spanWithoutSubsts is the source from start to end of the code in node n,
// with each command and process substitution shortened to $().
func spanWithoutSubsts(c *cmdCollector, n syntax.Node, start, end int) string {
	// The walk visits arguments before redirections, so the spans are
	// sorted into source order first.
	type span struct{ from, to int }
	var spans []span
	syntax.Walk(n, func(m syntax.Node) bool {
		switch m.(type) {
		case *syntax.CmdSubst, *syntax.ProcSubst:
			if from, to := int(m.Pos().Offset()), int(m.End().Offset()); from >= start && to <= end {
				spans = append(spans, span{from, to})
			}
			return false
		}
		return true
	})
	slices.SortFunc(spans, func(a, b span) int { return a.from - b.from })
	var b strings.Builder
	at := start
	for _, sp := range spans {
		if sp.from < at {
			continue
		}
		b.WriteString(c.src[at:sp.from])
		b.WriteString("$()")
		at = sp.to
	}
	b.WriteString(c.src[at:end])
	return b.String()
}

// readCode reads shell code from dirs for the targets it names, in a
// subshell.
func (e *targetEval) readCode(codes []string, dirs []string) []string {
	if e.depth >= 8 || e.reads+len(codes) > maxCodeReads {
		// Too deep, or too much code read again and again (bash -c
		// in heredocs in heredocs): too complex to follow.
		e.limited = true
		return nil
	}
	e.reads += len(codes)
	e.depth++
	defer func() { e.depth-- }()
	var end []string
	for _, code := range codes {
		e.sub(func() {
			_, f := e.readFlow(code, dirs)
			end = union(end, f.any)
		})
	}
	return end
}

// runsUnfollowedCode reports whether a command runs code that may push
// somewhere this does not follow: a shell or interpreter running anything but
// a script on disk (code from -c, a heredoc, a here-string, a pipe or the
// terminal), after a wrapper such as sudo or env too, wherever its name
// stands; a wrapper fed by a pipe (xargs); or a script, run by a shell or
// interpreter or by its path, that the line may have written.
func (e *targetEval) runsUnfollowedCode(s *syntax.Stmt, cmd *shellCmd, dirs []string) bool {
	starts := []int{0}
	if wrappers[cmd.name()] {
		// An option's value cannot be told from the program (env -u
		// bash bash -c ...), so every word that names one counts.
		starts = nil
		for j := 1; j < len(cmd.words); j++ {
			if w := path.Base(filepath.ToSlash(cmd.words[j])); shells[w] || interpreter(w) || w == "eval" {
				starts = append(starts, j)
			}
		}
		if len(starts) == 0 {
			// xargs: a program may run what a pipe, a heredoc or a
			// file gives it.
			return e.piped || slices.ContainsFunc(s.Redirs, func(r *syntax.Redirect) bool {
				return (r.N == nil || r.N.Value == "0") &&
					(r.Op == syntax.Hdoc || r.Op == syntax.DashHdoc || r.Op == syntax.WordHdoc || r.Op == syntax.RdrIn || r.Op == syntax.RdrInOut || r.Op == syntax.DplIn)
			})
		}
	}
	for _, j := range starts {
		words, dyn := cmd.words[j:], cmd.dyn[j:]
		name := path.Base(filepath.ToSlash(words[0]))
		args := argv{vals: words[1:], dyn: dyn[1:]}
		switch {
		case name == "eval":
			return true
		case shells[name]:
			_, _, inline, script, _ := codeArgs(args, shellCodeOption, "oO", map[string]bool{"--rcfile": true, "--init-file": true}, false)
			// bash -s arg reads its code from stdin; arg is no script.
			// After the first operand, -s is the script's own argument.
			// Whatever its options, a shell given a heredoc or here-string
			// with a script may be reading it as code.
			stdin := len(cmd.heredocs)+len(cmd.hereStrings) > 0
			for i := 0; i < len(args.vals); i++ {
				a := args.vals[i]
				if !strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "+") {
					break
				}
				// -s turns stdin mode on and +s off; the last one wins.
				if !strings.HasPrefix(a, "--") && strings.Contains(a, "s") {
					stdin = strings.HasPrefix(a, "-") || len(cmd.heredocs)+len(cmd.hereStrings) > 0
				}
				if takesOperand(strings.Replace(a, "+", "-", 1), "oO", map[string]bool{"--rcfile": true, "--init-file": true}) {
					// -o errexit, +o errexit: the value is no operand.
					i++
				}
			}
			if !script || inline || stdin || j > 0 || e.environs(s, cmd) || e.writes || dynamicScript(args, "oO", map[string]bool{"--rcfile": true, "--init-file": true}, modelledShells[name], false) {
				return true
			}
		case interpreter(name):
			_, _, inline, script, _ := codeArgs(args, interpreterCodeOption(name), interpreterValueShort(name), map[string]bool{"--require": true, "--import": true}, true)
			if !script || inline || j > 0 || e.environs(s, cmd) || e.writes || dynamicScript(args, interpreterValueShort(name), map[string]bool{"--require": true, "--import": true}, false, true) {
				return true
			}
		case cmd.byUnknownPath():
			return e.writes
		}
	}
	return false
}

// environs reports whether a command may run with variables or descriptors
// that may name or hold code a shell or interpreter runs before its script
// (BASH_ENV=/dev/fd/3, ENV, NODE_OPTIONS, PERL5OPT): an assignment before it,
// an export or exec on the line, one the gate inherits, or a heredoc,
// here-string or process substitution on any descriptor, or a redirection
// past stderr.
func (e *targetEval) environs(s *syntax.Stmt, cmd *shellCmd) bool {
	if len(cmd.assigns) > 0 || e.exports {
		return true
	}
	return slices.ContainsFunc(s.Redirs, func(r *syntax.Redirect) bool {
		if r.N != nil && r.N.Value != "0" && r.N.Value != "1" && r.N.Value != "2" {
			return true
		}
		switch r.Op {
		case syntax.Hdoc, syntax.DashHdoc, syntax.WordHdoc:
			return true
		}
		return r.Word != nil && slices.ContainsFunc(r.Word.Parts, func(p syntax.WordPart) bool {
			_, ok := p.(*syntax.ProcSubst)
			return ok
		})
	})
}

// modelledShells are the shells whose every short option taking a value
// dynamicScript knows (-o, -O).
var modelledShells = map[string]bool{"bash": true, "sh": true, "dash": true}

// startupVars name code a shell or interpreter runs before its script.
var startupVars = []string{"BASH_ENV", "ENV", "NODE_OPTIONS", "PERL5OPT", "RUBYOPT"}

// dynamicScript reports whether a shell's or interpreter's script, its first
// operand after the options and their values, is no script on disk: it holds
// an expansion (bash <(...), bash -o errexit "$f") or names stdin (bash
// /dev/stdin). The script's own arguments may hold expansions.
func dynamicScript(args argv, valueShort string, valueLong map[string]bool, shortKnown, dashIsStdin bool) bool {
	procSubst := func(w string) bool {
		return strings.Contains(w, "<(") || strings.Contains(w, ">(")
	}
	// /dev/stdin, /dev/fd/3, /proc/self/fd/0: a stream, which a pipe,
	// heredoc or process substitution may fill; no file on disk.
	streamed := func(w string, dyn bool) bool {
		return dyn || w == "-" || strings.HasPrefix(w, "/dev/") || strings.HasPrefix(w, "/proc/")
	}
	unknown := false
	for i := 0; i < len(args.vals); i++ {
		a := args.vals[i]
		if a == "-" && !dashIsStdin {
			// A shell's lone - ends its options, as -- does.
			continue
		}
		if !strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "+") || a == "-" {
			if streamed(a, args.dyn[i]) {
				return true
			}
			// Before this word, a process substitution may be code the
			// shell runs: an option's value (bash --rcfile <(...)).
			// After an option whose value this may not know, this word
			// may be that value (zsh --emulate sh <(...), fish -C true
			// <(...)), so one after it may be the script. Where every
			// short option is known (bash), after short ones this word
			// is the script and the rest its arguments.
			return slices.ContainsFunc(args.vals[:i], procSubst) || unknown && slices.ContainsFunc(args.vals[i+1:], procSubst)
		}
		if a != "--" && (!shortKnown || strings.HasPrefix(a, "--") || strings.HasPrefix(a, "++")) {
			// -- ends the options: the next word is the script.
			unknown = true
		}
		if name, value, ok := strings.Cut(a, "="); ok && strings.HasPrefix(a, "--") && valueLong[name] && streamed(value, args.dyn[i]) {
			// --rcfile=/dev/fd/3: code from a stream.
			return true
		}
		if takesOperand(strings.Replace(a, "+", "-", 1), valueShort, valueLong) {
			// An option's value from a stream may be code the shell runs
			// before the script (bash --rcfile /dev/fd/3).
			if i+1 < len(args.vals) && streamed(args.vals[i+1], args.dyn[i+1]) {
				return true
			}
			i++
		}
	}
	// Before the script, a process substitution may be code the shell
	// runs: an option's value (bash --rcfile <(...)).
	return slices.ContainsFunc(args.vals, procSubst)
}

// writesFiles reports whether a command line may write a file, such as a
// script it then runs: an output redirection to a file, or a command not
// known to write none. Only text the line holds matters, which a heredoc,
// here-string or argument may carry into a file (tee, git apply, sed -i,
// ed), so a script run from disk counts as writing none: what it writes
// comes from disk, as any push it makes would.
func writesFiles(command string) bool {
	file, err := shellParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return true
	}
	c := &cmdCollector{src: command}
	writes := false
	syntax.Walk(file, func(n syntax.Node) bool {
		if r, ok := n.(*syntax.Redirect); ok {
			target, _ := c.word(r.Word)
			switch r.Op {
			case syntax.RdrOut, syntax.AppOut, syntax.RdrInOut, syntax.RdrClob, syntax.AppClob, syntax.RdrAll, syntax.AppAll, syntax.RdrAllClob, syntax.AppAllClob:
				writes = writes || target != "/dev/null"
			case syntax.DplOut:
				// >&file is a file unless it names a descriptor.
				writes = writes || target != "-" && strings.Trim(target, "0123456789") != ""
			}
		}
		return !writes
	})
	if writes {
		return true
	}
	cmds, _ := parseShell(command, false)
	for _, cmd := range cmds {
		if len(cmd.words) > 0 && !writesNone(cmd) {
			return true
		}
	}
	return false
}

// writesNone reports whether a command is known to write no file from text
// it is given: the plain readers, echo and printf, the builtins
// that move the shell, the git and gh builtins known to write none, and a shell or
// interpreter, which runs a script from disk or code read elsewhere.
func writesNone(cmd *shellCmd) bool {
	if cmd.dyn[0] || len(cmd.assigns) > 0 {
		// GIT_PAGER='tee x' git log.
		return false
	}
	name := cmd.name()
	switch {
	case name == "git":
		return gitWritesNone(cmd.args())
	case name == "gh":
		// An alias may run shell code (gh alias set --shell); one cannot
		// shadow a builtin.
		args := cmd.args().vals
		i := slices.IndexFunc(args, func(a string) bool { return !strings.HasPrefix(a, "-") })
		return i >= 0 && ghQuiet[args[i]] && !slices.ContainsFunc(cmd.dyn, func(d bool) bool { return d })
	case plainReaders[name], shellMoves[name], quietCommands[name], shells[name], interpreter(name):
		return true
	}
	return false
}

// gitWritesNone reports whether a git command is a builtin known to write no
// file from text it is given. An unknown subcommand may be an alias that
// runs shell code (alias.save = !tee x), and -c may set one; --output and
// apply, am or format-patch write files.
func gitWritesNone(args argv) bool {
	i, ok := gitSubcommand(args)
	if !ok {
		return false
	}
	return i == len(args.vals) || gitQuiet[args.vals[i]] && !slices.ContainsFunc(args.vals[i+1:], func(w string) bool {
		return strings.HasPrefix(w, "--output") || strings.HasPrefix(w, "-o")
	})
}

// gitSubcommand finds a git command's subcommand past its global options,
// reporting false for -c or an expansion, which may make it anything.
func gitSubcommand(args argv) (int, bool) {
	for i := 0; i < len(args.vals); i++ {
		a := args.vals[i]
		switch {
		case args.dyn[i] || a == "-c" || strings.HasPrefix(a, "-c") && !strings.HasPrefix(a, "--"):
			return 0, false
		case a == "-C" || a == "--git-dir" || a == "--work-tree" || a == "--namespace":
			i++
		case strings.HasPrefix(a, "-"):
		default:
			return i, true
		}
	}
	return len(args.vals), true
}

// pushText reports whether text on a command line names a push outside a
// command that is itself one: a heredoc, a here-string, or another
// command's argument (git commit -m, echo), which may reach a file the line
// then runs (.git/COMMIT_EDITMSG).
func pushText(command string) bool {
	cmds, _ := parseShell(command, false)
	for _, cmd := range cmds {
		if slices.ContainsFunc(append(slices.Clone(cmd.heredocs), cmd.hereStrings...), namesPush) {
			return true
		}
		if len(cmd.words) < 2 {
			continue
		}
		args := cmd.args()
		switch cmd.name() {
		case "git":
			if i, ok := gitSubcommand(args); ok && i < len(args.vals) && args.vals[i] == "push" {
				continue
			}
		case "gh":
			if len(args.vals) > 1 && args.vals[0] == "pr" && args.vals[1] == "create" {
				continue
			}
		}
		if namesPush(strings.Join(args.vals, " ")) {
			return true
		}
	}
	return false
}

// gitQuiet is the git builtins that write no file from text the line gives
// them; what they write comes from the repository or a remote. Not rebase
// --exec, which runs a command, nor config, which sets a pager, editor or
// alias later commands run.
var gitQuiet = map[string]bool{
	"push": true, "status": true, "log": true, "diff": true, "show": true, "fetch": true, "pull": true,
	"rev-parse": true, "branch": true, "remote": true, "ls-files": true, "describe": true, "tag": true,
	"add": true, "commit": true, "blame": true, "grep": true, "shortlog": true, "symbolic-ref": true,
	"for-each-ref": true, "cat-file": true, "merge-base": true, "rev-list": true, "ls-remote": true,
	"switch": true, "checkout": true, "restore": true, "reset": true, "stash": true,
	"merge": true, "cherry-pick": true, "revert": true, "init": true, "clone": true,
	"worktree": true, "mv": true, "rm": true, "help": true, "version": true,
}

// ghQuiet is the gh builtins that write no file from text the line gives
// them.
var ghQuiet = map[string]bool{
	"pr": true, "issue": true, "api": true, "auth": true, "repo": true, "run": true, "workflow": true,
	"release": true, "browse": true, "status": true, "search": true, "label": true,
}

// plainReaders read files and write only to stdout, with no option that
// writes a file or runs a command: not sort -o, uniq in out, tree -o, less
// -o, file -C, bat --pager, rg --pre.
var plainReaders = map[string]bool{
	"cat": true, "head": true, "tail": true, "wc": true, "nl": true, "ls": true, "diff": true, "cmp": true,
	"stat": true, "du": true, "tr": true, "cut": true, "column": true, "fold": true, "rev": true, "tac": true,
	"paste": true, "fmt": true, "expand": true, "unexpand": true, "jq": true, "grep": true, "egrep": true, "fgrep": true,
}

// quietCommands write no file.
var quietCommands = map[string]bool{
	"echo": true, "printf": true, "test": true, "[": true, "pwd": true, "sleep": true, "which": true,
	"type": true, "exit": true, "return": true, "read": true, "wait": true, "slopcop": true,
}

// setsCDPATH reports whether a statement may set CDPATH to something, outside
// a subshell, a pipeline or a background job: CDPATH=x, export CDPATH=x, read
// CDPATH. CDPATH=x cmd sets it for cmd alone. Clearing it is not followed, as
// a clear may not run (false && CDPATH=), so once set it stays set.
func setsCDPATH(c *cmdCollector, s *syntax.Stmt) bool {
	if s.Background {
		return false
	}
	nonEmpty := func(a *syntax.Assign) bool {
		if a.Name == nil || a.Name.Value != "CDPATH" || a.Value == nil {
			return false
		}
		value, dyn := c.word(a.Value)
		return value != "" || dyn
	}
	// Only the statement's own command: the statements inside a compound
	// one are read, and judged, each in turn.
	switch n := s.Cmd.(type) {
	case *syntax.CallExpr:
		if len(n.Args) == 0 {
			return slices.ContainsFunc(n.Assigns, nonEmpty)
		}
		return len(n.Args) > 1 && n.Args[0].Lit() == "read" && slices.ContainsFunc(n.Args[1:], func(w *syntax.Word) bool { return w.Lit() == "CDPATH" })
	case *syntax.DeclClause:
		return slices.ContainsFunc(n.Args, nonEmpty)
	}
	return false
}

// cdFlow is the flow of a cd, pushd or popd, and whether the words are one.
// It moves the commands after it where it succeeds; in a list, it is taken to
// succeed into a directory that exists, and otherwise may have failed.
func cdFlow(dirs, words []string, cdpath bool) (flow, bool) {
	var moved, after []string
	for _, dir := range dirs {
		target, ok := cdTarget(dir, words)
		if !ok {
			return flow{}, false
		}
		if arg := words[len(words)-1]; cdpath && len(words) > 1 && !filepath.IsAbs(arg) && !strings.HasPrefix(arg, "~") && !strings.HasPrefix(arg, "-") &&
			arg != "." && arg != ".." && !strings.HasPrefix(arg, "./") && !strings.HasPrefix(arg, "../") {
			// CDPATH may send cd repo, or cd .repo, to a directory
			// elsewhere; only ., .. and paths from them skip it.
			target = unknownDir
		}
		moved = union(moved, []string{target})
		if target != unknownDir && isDir(target) {
			after = union(after, []string{target})
		} else {
			// Missing now, the cd fails and the next command runs where
			// it was, unless an earlier command on the line makes the
			// directory (mkdir, cp -r): both count.
			after = union(after, []string{dir, target})
		}
	}
	return flow{succ: moved, any: union(dirs, moved), after: after}, true
}

// shellRun is shell code a command runs.
type shellRun struct {
	// code is -c code or eval's arguments; with fromStdin, the shell
	// reads its code from stdin instead.
	code      string
	fromStdin bool
	// inShell says the code runs in this shell (eval), not a new one.
	inShell bool
	// stdin is the heredocs and here-strings the shell reads, any of which
	// may be the one it gets (only the last is).
	stdin []string
}

// shellRuns is the shell code a command runs: a shell's -c code or its
// stdin, or eval's arguments. After a wrapper such as sudo or env, every
// word that names a shell or eval counts, as an option's value (env -u git
// bash -c ...) cannot be told from the program; a word that is not one
// (env git push origin sh) only adds code to read.
func shellRuns(cmd *shellCmd) []shellRun {
	starts := []int{0}
	wrapped := wrappers[cmd.name()]
	if wrapped {
		starts = nil
		for j := 1; j < len(cmd.words); j++ {
			if w := path.Base(filepath.ToSlash(cmd.words[j])); w == "eval" || shells[w] {
				starts = append(starts, j)
			}
		}
	}
	stdin := append(slices.Clone(cmd.heredocs), cmd.hereStrings...)
	var runs []shellRun
	for _, j := range starts {
		words, dyn := cmd.words[j:], cmd.dyn[j:]
		name := path.Base(filepath.ToSlash(words[0]))
		args := argv{vals: words[1:], dyn: dyn[1:]}
		switch {
		case name == "eval" && words[0] == "eval":
			// eval runs in this shell; after a wrapper (env eval), not.
			runs = append(runs, shellRun{code: strings.Join(args.vals, " "), inShell: !wrapped, stdin: stdin})
		case shells[name]:
			code, _, inline, script, _ := codeArgs(args, shellCodeOption, "oO", map[string]bool{"--rcfile": true, "--init-file": true}, false)
			switch {
			case inline:
				runs = append(runs, shellRun{code: code, stdin: stdin})
			case !script:
				runs = append(runs, shellRun{fromStdin: true, stdin: stdin})
			}
		}
	}
	return runs
}

// isDir reports whether path is a directory now.
func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// union is a followed by the directories of b it lacks.
func union(a, b []string) []string {
	out := append([]string(nil), a...)
	for _, dir := range b {
		found := false
		for _, have := range out {
			if have == dir {
				found = true
				break
			}
		}
		if !found {
			out = append(out, dir)
		}
	}
	return out
}

// maxNamedDirs bounds how many directories namedDirs reports, so a long
// command line costs a few git calls at most.
const maxNamedDirs = 32

// maxResolves bounds the paths namedDirs tries, a word from a directory.
const maxResolves = 4096

// namedDirs lists the existing directories a command line names as
// arguments, read from cwd, such as the source of `cp -r /repo copy`, and in
// shell code it runs (bash -c "git -C /repo push"). When a push's own
// directory cannot be read, a guarded repository among them still arms the
// gate.
//
// It also reports whether it stopped short of reading all the code, past its
// bounds: a line too complex to read for every directory it may name.
func namedDirs(cwd, command string) ([]string, bool) {
	// The words that may name a directory are gathered first, then
	// resolved from wherever the line may have gone, and from each
	// directory found, in turn: cd /parent; f; cd sub; cd repo reaches
	// /parent/sub/repo.
	var words []string
	seenWord := map[string]bool{}
	consider := func(word string) {
		if _, value, ok := strings.Cut(word, "="); ok && strings.HasPrefix(word, "-") {
			// --git-dir=/repo/.git
			word = value
		}
		if word != "" && !strings.HasPrefix(word, "-") && !seenWord[word] {
			seenWord[word] = true
			words = append(words, word)
		}
	}
	parsed := map[string]bool{}
	limited := false
	var visit func(src string, depth int)
	visit = func(src string, depth int) {
		if parsed[src] {
			// Read already: a nested heredoc is in its parent's commands.
			return
		}
		if len(parsed) >= maxCodeReads {
			limited = true
			return
		}
		parsed[src] = true
		cmds, err := parseShell(src, false)
		if err != nil {
			// The parser stopped early: read the rest by words too.
			for _, seg := range segments(src) {
				words := shellWords(seg.text)
				for i := 1; i < len(words); i++ {
					consider(words[i])
				}
			}
		}
		for _, cmd := range cmds {
			// A heredoc or here-string may be code a shell runs, or a
			// script the line writes: { bash; } <<EOF, tee x.sh <<EOF.
			for _, code := range append(slices.Clone(cmd.heredocs), cmd.hereStrings...) {
				if depth >= 8 {
					limited = true
					break
				}
				visit(code, depth+1)
			}
			for i := 1; i < len(cmd.words); i++ {
				consider(cmd.words[i])
				if strings.ContainsAny(cmd.words[i], " \t\n") {
					// Code in one quoted word, such as python3 -c
					// "...", may name a directory too.
					for _, word := range shellWords(cmd.words[i]) {
						consider(word)
					}
				}
			}
			if len(cmd.words) > 0 {
				for _, run := range shellRuns(cmd) {
					if run.code == "" {
						continue
					}
					if depth >= 8 {
						limited = true
						break
					}
					visit(run.code, depth+1)
				}
			}
		}
	}
	visit(command, 0)
	var dirs []string
	bases := union([]string{cwd}, visitedDirs(cwd, command))
	// Each round adds a directory or stops, so maxNamedDirs bounds it, and
	// maxResolves bounds the paths it tries.
	tries, absDone := 0, false
	for round := 0; len(bases) > 0 && len(dirs) < maxNamedDirs && !limited; round++ {
		var found []string
		full := func() bool { return len(dirs)+len(found) >= maxNamedDirs || limited }
		for _, base := range bases {
			if base == unknownDir || full() {
				continue
			}
			for _, word := range words {
				if filepath.IsAbs(word) && absDone {
					// An absolute path resolves the same from any base.
					continue
				}
				if tries++; tries > maxResolves {
					limited = true
					break
				}
				if dir := resolveDir(base, word); dir != unknownDir && isDir(dir) && !slices.Contains(dirs, dir) {
					found = union(found, []string{dir})
					if filepath.Base(dir) == ".git" {
						found = union(found, []string{filepath.Dir(dir)})
					}
				}
				if full() {
					break
				}
			}
			absDone = true
		}
		found = slices.DeleteFunc(found, func(d string) bool { return slices.Contains(dirs, d) })
		if len(dirs)+len(found) > maxNamedDirs {
			found = found[:max(0, maxNamedDirs-len(dirs))]
		}
		dirs = append(dirs, found...)
		bases = found
	}
	return dirs, limited
}
