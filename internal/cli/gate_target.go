package cli

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
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
// the line names (the source of a cp -r). Like pushCommand, this guards against an agent's slip; it is not a
// shell parser or a security boundary.

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
)

// pushTargets lists the directories the pushes on a command line may run in,
// starting from cwd, and why some push may run in a directory it cannot read
// ("" when it read them all). Where the shell could run a push in more than
// one directory, it lists each: after `cd dir && make || <push>`, the push
// runs in the old directory when the cd failed and in dir when make did.
func pushTargets(cwd, command string) ([]string, string) {
	targets, unsure := pushTargetsIn(cwd, command, 0)
	if stripped, ok := withoutHeredocs(command); ok {
		// segments reads a heredoc's lines as commands, and a quote in
		// its text can swallow the commands after it. The line is read
		// again without its heredocs, and a push counts where either
		// reading puts it.
		more, why := pushTargetsIn(cwd, stripped, 0)
		targets = union(targets, more)
		if why == unsureShell || why != "" && unsure == "" {
			unsure = why
		}
	}
	return targets, unsure
}

// pushTargetsIn is pushTargets for a command line, or for shell code
// (bash -c, eval) one runs, depth levels down.
func pushTargetsIn(cwd, command string, depth int) ([]string, string) {
	// dirs is where the next command may run. been is every directory the
	// current and-or list (commands joined by && and ||) has run in: a
	// command after ||, and the first command after the list, run wherever
	// the list stopped, which is any of them. Once a list has an ||, whether
	// a later cd ran depends on what failed, so branched makes each cd add
	// its directory rather than replace the others. A list of one cd needs
	// none of this: whether it moves is read from its directory existing.
	dirs := []string{cwd}
	been := dirs
	branched := false
	length := 0
	// frames holds the state each open subshell or command substitution
	// started in, and whether it opened with a backtick.
	type frame struct {
		dirs, been []string
		branched   bool
		length     int
		backtick   bool
	}
	var frames []frame
	var targets []string
	unsure := ""
	seen := map[string]bool{}
	// attributed counts the pushes given a directory, whether or not it
	// was a new one.
	attributed := 0
	add := func(target string) {
		attributed++
		switch {
		case target == unknownDir:
			unsure = unsureShell
		case !isDir(target):
			// A directory missing now may be one the same command makes
			// (git worktree add, cp -r, git clone), which the gate cannot
			// judge before it exists.
			if unsure == "" {
				unsure = unsureMissing
			}
		case !seen[target]:
			seen[target] = true
			targets = append(targets, target)
		}
	}
	// segments reads a heredoc's lines as commands, so a cd in its text
	// may move dirs where the shell does not go. On a line with a heredoc,
	// each push is judged in every directory the line has been in, which
	// holds the one it really runs in.
	heredoc := hasHeredoc(command)
	visited := dirs
	prev := ""
	for _, seg := range segments(command) {
		pushDirs := dirs
		if heredoc {
			pushDirs = union(visited, dirs)
		}
		// A pipeline runs each of its commands in a subshell.
		piped := prev == "|" || seg.next == "|"
		prev = seg.next
		words := shellWords(seg.text)
		// quotedPush also finds a push whose words are quoted, such as
		// git "push"; name tells its git from its gh.
		name, args := commandName(seg.text)
		quoted := pushWords(name, args)
		codes, nested := shellCode(seg.text)
		before := attributed
		switch {
		case nested && depth < 8 && slices.ContainsFunc(codes, func(code string) bool { return pushIn(code, depth+1) }):
			// bash -c 'git -C dir push' and eval: the pushes in the code
			// run where it says, from where the shell runs. This reader
			// may misread the code (a heredoc's text, code from stdin), so
			// the shell's own directory is judged too, as for any push.
			for _, dir := range pushDirs {
				add(dir)
				if dir == unknownDir {
					continue
				}
				for _, code := range codes {
					inner, why := pushTargetsIn(dir, code, depth+1)
					for _, target := range inner {
						add(target)
					}
					if why == unsureShell || why != "" && unsure == "" {
						unsure = why
					}
				}
			}
		case gitPush.MatchString(seg.text) || quoted && name == "git":
			for _, dir := range pushDirs {
				add(gitDir(dir, words))
			}
		case ghPRCreate.MatchString(seg.text) || quoted:
			for _, dir := range pushDirs {
				add(dir)
			}
		default:
			if moved, ok := cdDirs(dirs, words, seg.next, branched, piped); ok {
				dirs = moved
			}
		}
		// A push the parser finds in this command that none of the
		// cases above placed (time -p bash -c '...', and forms not yet
		// followed) runs somewhere the gate cannot read.
		if attributed == before && depth < 8 && pushIn(seg.text, depth+1) {
			unsure = unsureShell
		}
		been = union(been, dirs)
		visited = union(visited, dirs)
		if strings.TrimSpace(seg.text) != "" {
			length++
		}
		n := len(frames)
		switch {
		case seg.next == "||":
			// The next command runs when this one failed, which may be in
			// any directory the list has been in.
			dirs = been
			branched = true
		case seg.next == "&&":
		case seg.next == "|":
			// A pipeline is one command of its and-or list: what runs after
			// it, joined by && or ||, runs where the list was, not where an
			// earlier list stopped. cdDirs already keeps a piped cd from
			// moving anything.
		case seg.next == "(":
			// A subshell or command substitution keeps its cd to itself.
			frames = append(frames, frame{dirs: dirs, been: been, branched: branched, length: length})
			been, branched, length = dirs, false, 0
		case seg.next == ")" && n > 0 && !frames[n-1].backtick:
			// The subshell is one command of the list around it.
			f := frames[n-1]
			dirs, been, branched, length, frames = f.dirs, f.been, f.branched, f.length+1, frames[:n-1]
		case seg.next == "`" && n > 0 && frames[n-1].backtick:
			f := frames[n-1]
			dirs, been, branched, length, frames = f.dirs, f.been, f.branched, f.length, frames[:n-1]
		case seg.next == "`":
			frames = append(frames, frame{dirs: dirs, been: been, branched: branched, length: length, backtick: true})
			been, branched, length = dirs, false, 0
		default:
			// ;, a newline or &: a new list starts here, wherever the
			// last one stopped.
			if length > 1 {
				dirs = been
			}
			been, branched, length = dirs, false, 0
		}
	}
	return targets, unsure
}

// withoutHeredocs is a command line with each heredoc, its operator,
// delimiter and body, taken out as the parser reads them, and whether it
// had one the parser could read.
func withoutHeredocs(command string) (string, bool) {
	file, err := shellParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return "", false
	}
	type span struct{ start, end int }
	var spans []span
	syntax.Walk(file, func(n syntax.Node) bool {
		r, ok := n.(*syntax.Redirect)
		if !ok || r.Op != syntax.Hdoc && r.Op != syntax.DashHdoc {
			return true
		}
		spans = append(spans, span{int(r.OpPos.Offset()), int(r.Word.End().Offset())})
		if r.Hdoc != nil && len(r.Hdoc.Parts) > 0 {
			spans = append(spans, span{int(r.Hdoc.Parts[0].Pos().Offset()), int(r.Hdoc.End().Offset())})
		}
		return true
	})
	if len(spans) == 0 {
		return "", false
	}
	slices.SortFunc(spans, func(a, b span) int { return a.start - b.start })
	var out strings.Builder
	at := 0
	for _, sp := range spans {
		if sp.start < at {
			// A heredoc inside another's body went with it.
			continue
		}
		out.WriteString(command[at:sp.start])
		at = sp.end
	}
	out.WriteString(command[at:])
	return out.String(), true
}

// hasHeredoc reports whether a command line has a heredoc, as the parser
// reads it, or, where the parser cannot read it, a << that is not <<<.
func hasHeredoc(command string) bool {
	file, err := shellParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return strings.Contains(strings.ReplaceAll(command, "<<<", ""), "<<")
	}
	found := false
	syntax.Walk(file, func(n syntax.Node) bool {
		if r, ok := n.(*syntax.Redirect); ok && (r.Op == syntax.Hdoc || r.Op == syntax.DashHdoc) {
			found = true
		}
		return !found
	})
	return found
}

// leadingKeywords matches the keywords segments leaves before a command.
var leadingKeywords = regexp.MustCompile(`^\s*(?:(?:if|then|else|elif|do|while|until|time|!|\{)\s+)+`)

// shellCode is the code one command of a line may run as shell code: a
// shell's -c code, each of its heredocs and here-strings, or eval's
// arguments, after a wrapper such as sudo.
func shellCode(text string) ([]string, bool) {
	// A segment may start with the keyword before a command: then, do.
	text = leadingKeywords.ReplaceAllString(text, "")
	cmds, err := parseShell(text, false)
	if err != nil || len(cmds) == 0 || len(cmds[0].words) == 0 {
		return nil, false
	}
	cmd := cmds[0]
	words, dyn := cmd.words, cmd.dyn
	if j := slices.IndexFunc(words, func(w string) bool {
		w = path.Base(filepath.ToSlash(w))
		return w == "eval" || shells[w]
	}); j >= 0 && (j == 0 || wrappers[cmd.name()]) {
		words, dyn = words[j:], dyn[j:]
	} else {
		return nil, false
	}
	args := argv{vals: words[1:], dyn: dyn[1:]}
	if path.Base(filepath.ToSlash(words[0])) == "eval" {
		return []string{strings.Join(args.vals, " ")}, true
	}
	code, _, inline, script, _ := codeArgs(args, shellCodeOption, "oO", map[string]bool{"--rcfile": true, "--init-file": true}, false)
	if inline {
		return []string{code}, true
	}
	// bash <<< '...' and bash <<EOF: the shell reads its code from stdin.
	// Of several, only the last is stdin; each is followed on its own, so
	// no other's cd counts.
	if sources := append(slices.Clone(cmd.heredocs), cmd.hereStrings...); !script && len(sources) > 0 {
		return sources, true
	}
	return nil, false
}

// cdDirs is where the commands after a cd may run, from each directory in
// dirs, and whether the segment is a cd that moves them. A cd surely moves
// the commands after it when it comes before && (they run only if it
// succeeded), or before ; or a newline into a directory that exists, in a list
// with no || so far. Where it may or may not have run, before || or after one,
// or into a directory that cannot be read, its directory joins the others. A
// cd in a pipeline or a background job moves nothing.
func cdDirs(dirs, words []string, next string, branched, piped bool) ([]string, bool) {
	if piped || next == "&" {
		return nil, false
	}
	// moved is where the cd goes from each directory; after is where the
	// next command runs from each, read from whether that cd succeeds.
	var moved, after []string
	unknown := false
	for _, dir := range dirs {
		target, ok := cdTarget(dir, words)
		if !ok {
			return nil, false
		}
		moved = union(moved, []string{target})
		switch {
		case target == unknownDir:
			unknown = true
			after = union(after, []string{dir, target})
		case isDir(target):
			after = union(after, []string{target})
		default:
			// Missing now, the cd fails and the next command runs where it
			// was, unless an earlier command on the line makes the
			// directory (mkdir, cp -r): both count.
			after = union(after, []string{dir, target})
		}
	}
	switch {
	case branched || next == "||" || unknown:
		return union(dirs, moved), true
	case next == "&&":
		// The next command runs only where the cd succeeded.
		return moved, true
	default:
		return after, true
	}
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
const maxNamedDirs = 16

// namedDirs lists the existing directories a command line names as
// arguments, read from cwd, such as the source of `cp -r /repo copy`. When a
// push's own directory cannot be read, a guarded repository among them still
// arms the gate.
func namedDirs(cwd, command string) []string {
	var dirs []string
	for _, seg := range segments(command) {
		words := shellWords(seg.text)
		var candidates []string
		for i := 1; i < len(words); i++ {
			candidates = append(candidates, words[i])
			if strings.ContainsAny(words[i], " \t\n") {
				// Code in one quoted word, as in bash -c "git -C /repo
				// push", names its directories too.
				candidates = append(candidates, shellWords(words[i])...)
			}
		}
		for _, word := range candidates {
			if len(dirs) >= maxNamedDirs {
				break
			}
			if strings.HasPrefix(word, "-") {
				continue
			}
			if dir := resolveDir(cwd, word); dir != unknownDir && isDir(dir) {
				dirs = union(dirs, []string{dir})
			}
		}
	}
	return dirs
}
