package cli

import (
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// The push gate's two shell checks, forgedApproval and pushCommand, read a
// command line through mvdan.cc/sh's parser, the one behind shfmt, rather
// than by hand: quotes, heredocs, comments, arithmetic, here-strings,
// subshells and command substitution are the parser's job. parseShell flattens
// the syntax tree into the simple commands the shell would run, each with its
// words, redirections and stdin; the checks judge one command at a time.

// shellCmd is one simple command of a command line, as the shell would run
// it once it has removed quotes: an expansion ($x, $(...), $((...))) stays as
// written and marks its word dynamic.
type shellCmd struct {
	// raw is the command's source without its heredoc bodies: assignments,
	// words and redirections.
	raw string
	// masked is the command's words with the inside of every quoted part
	// blanked, so a pattern matched over it sees only the words the shell
	// runs and the shape of each quoted word.
	masked string
	// assigns are the NAME=value words before the command, rendered.
	assigns []string
	// assignDyn says an assignment's value holds an expansion.
	assignDyn []bool
	// words are the command name and its arguments, after any command or
	// builtin prefix, rendered.
	words []string
	// bypass says a command or builtin prefix was taken off, so the name
	// does not run a function.
	bypass bool
	// dyn says a word holds an expansion, or a glob or brace that may
	// reach a dot file, so its value is not known before the shell runs.
	dyn []bool
	// redirs are the files the command's redirections read or write.
	redirs []string
	// redirDyn says a redirection's file holds an expansion.
	redirDyn []bool
	// heredocs are the bodies of the command's heredocs.
	heredocs []string
	// hereStrings are the texts of its here-strings (<<< word).
	hereStrings []string
	// pipe says the command's stdin is a pipe, or the stdin of an enclosing
	// compound command fed by a heredoc or here-string.
	pipe bool
	// heredocExpands says an unquoted heredoc body holds an expansion,
	// so the text the command reads is not the text written, and
	// heredocSubst that one is a command substitution.
	heredocExpands, heredocSubst bool
	// unsure says the parser and the shell may disagree on where one of
	// its heredocs ends: a delimiter that is not a plain word, or an
	// unquoted body that continues a line with a backslash.
	unsure bool
}

// stdin is the text the command reads on stdin from its heredocs and
// here-strings, one per line.
func (c *shellCmd) stdin() string {
	var b strings.Builder
	for _, s := range c.heredocs {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	for _, s := range c.hereStrings {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	return b.String()
}

// name is the base name of the command, or "" for a command with no words.
// A word that is empty or "." stays as it is: path.Base would turn "" into
// ".", the source builtin.
func (c *shellCmd) name() string {
	if len(c.words) == 0 {
		return ""
	}
	if w := c.words[0]; w != "" && w != "." {
		return path.Base(filepath.ToSlash(w))
	}
	return c.words[0]
}

// byUnknownPath reports whether the command is run by a path outside the
// system's program directories (./cat, /tmp/x), so it may be a script the
// line wrote under a known tool's name.
func (c *shellCmd) byUnknownPath() bool {
	if len(c.words) == 0 || !strings.ContainsAny(c.words[0], `/\`) {
		return false
	}
	return !systemProgram.MatchString(filepath.ToSlash(c.words[0]))
}

// systemProgram matches a program in a system or package manager's program
// directory, where a command line does not write.
var systemProgram = regexp.MustCompile(`^/(?:usr/(?:local/)?)?s?bin/[^/]+$|^/opt/homebrew/bin/[^/]+$`)

// argv is a command's arguments and which of them are dynamic.
type argv struct {
	vals []string
	dyn  []bool
}

// args is the command's arguments after its name.
func (c *shellCmd) args() argv {
	if len(c.words) == 0 {
		return argv{}
	}
	return argv{vals: c.words[1:], dyn: c.dyn[1:]}
}

// sub is arguments i to j.
func (a argv) sub(i, j int) argv {
	return argv{vals: a.vals[i:j], dyn: a.dyn[i:j]}
}

// protected reports whether argument i, read as a path, names the approval
// refs, the state directory or anything under .git, or may expand to one.
func (a argv) protected(i int) bool {
	return a.dyn[i] || protectedPath(a.vals[i])
}

// anyProtected reports whether any argument but the text ones is protected.
func (a argv) anyProtected(text map[int]bool) bool {
	for i := range a.vals {
		if !text[i] && a.protected(i) {
			return true
		}
	}
	return false
}

// shellParser reads Bash, the shell the agents' tools run.
func shellParser() *syntax.Parser {
	return syntax.NewParser(syntax.Variant(syntax.LangBash))
}

// parseShell parses a command line into the simple commands it runs, in the
// order they appear; commands in a command substitution come after the
// command that holds it. pipe says the line's stdin is a pipe, as for code a
// shell reads from one. When the line has a syntax error, it returns the
// error with the commands of the statements before it, which the shell runs
// before it reports the error.
func parseShell(src string, pipe bool) ([]*shellCmd, error) {
	c := &cmdCollector{src: src}
	file, err := shellParser().Parse(strings.NewReader(src), "")
	if err == nil {
		c.walk(file, pipe)
		return c.cmds, nil
	}
	for stmt, stmtErr := range shellParser().StmtsSeq(strings.NewReader(src)) {
		if stmtErr != nil {
			break
		}
		c.walk(stmt, pipe)
	}
	return c.cmds, err
}

// cmdCollector gathers the simple commands of a syntax tree.
type cmdCollector struct {
	src  string
	cmds []*shellCmd
}

// text is the source of a node.
func (c *cmdCollector) text(n syntax.Node) string {
	return c.src[n.Pos().Offset():n.End().Offset()]
}

// walk visits a subtree; pipe says its commands read a pipe. The right side
// of a pipe reads it, as does everything in it: printf ... | (cd x; sh).
func (c *cmdCollector) walk(node syntax.Node, pipe bool) {
	syntax.Walk(node, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.Stmt:
			c.stmt(n, pipe)
			return false
		case *syntax.BinaryCmd:
			if n.Op == syntax.Pipe || n.Op == syntax.PipeAll {
				c.walk(n.X, pipe)
				c.walk(n.Y, true)
				return false
			}
		}
		return true
	})
}

// stmt gathers a statement: its simple command, if it is one, and the
// commands inside it.
func (c *cmdCollector) stmt(s *syntax.Stmt, pipe bool) {
	cmd, inner := c.simple(s, pipe)
	if cmd != nil {
		c.cmds = append(c.cmds, cmd)
	}
	switch x := s.Cmd.(type) {
	case *syntax.CallExpr, *syntax.DeclClause, *syntax.TestClause, *syntax.ArithmCmd, *syntax.LetClause:
		c.walk(x, pipe)
	default:
		if x != nil {
			c.walk(x, inner)
		}
	}
	// Command substitutions in redirections and unquoted heredoc bodies run
	// as the shell reads them.
	for _, r := range s.Redirs {
		if r.Word != nil {
			c.walk(r.Word, pipe)
		}
		if r.Hdoc != nil {
			c.walk(r.Hdoc, pipe)
		}
	}
}

// simple is a statement's simple command, without the commands inside it,
// or for a compound command its redirections as a command with no words
// (nil when it has none); and whether the commands inside it read a pipe
// or the compound command's heredoc or here-string.
func (c *cmdCollector) simple(s *syntax.Stmt, pipe bool) (*shellCmd, bool) {
	cmd := &shellCmd{pipe: pipe}
	var raw []string
	for _, r := range s.Redirs {
		raw = append(raw, c.redirect(cmd, r))
	}
	// A heredoc or here-string on a compound command is the stdin of the
	// commands in it.
	inner := pipe || len(cmd.heredocs) > 0 || len(cmd.hereStrings) > 0
	switch x := s.Cmd.(type) {
	case *syntax.CallExpr:
		var masked []string
		for _, a := range x.Assigns {
			text, dyn := c.assign(a)
			cmd.assigns = append(cmd.assigns, text)
			cmd.assignDyn = append(cmd.assignDyn, dyn)
		}
		for _, w := range x.Args {
			text, dyn := c.word(w)
			cmd.words = append(cmd.words, text)
			cmd.dyn = append(cmd.dyn, dyn)
			masked = append(masked, c.mask(w))
		}
		// command and builtin run the next word in the same shell.
		for len(cmd.words) > 0 && (cmd.words[0] == "command" || cmd.words[0] == "builtin") {
			isCommand := cmd.words[0] == "command"
			cmd.words, cmd.dyn, masked = cmd.words[1:], cmd.dyn[1:], masked[1:]
			cmd.bypass = true
			// command -p runs the command with a default PATH (builtin
			// has no -p); -- ends the options, so a -p after it is the
			// command.
			for isCommand && len(cmd.words) > 0 && cmd.words[0] == "-p" {
				cmd.words, cmd.dyn, masked = cmd.words[1:], cmd.dyn[1:], masked[1:]
			}
			if len(cmd.words) > 0 && cmd.words[0] == "--" {
				cmd.words, cmd.dyn, masked = cmd.words[1:], cmd.dyn[1:], masked[1:]
			}
		}
		cmd.raw = c.rawText(x, raw)
		cmd.masked = strings.Join(masked, " ")
	case *syntax.DeclClause:
		// export, local, declare: judged as a command named after the
		// keyword, whose arguments are its assignments.
		cmd.words = []string{x.Variant.Value}
		cmd.dyn = []bool{false}
		for _, a := range x.Args {
			text, dyn := c.assign(a)
			cmd.words = append(cmd.words, text)
			cmd.dyn = append(cmd.dyn, dyn)
		}
		cmd.raw = c.rawText(x, raw)
		cmd.masked = cmd.raw
	case *syntax.TestClause, *syntax.ArithmCmd, *syntax.LetClause:
		// [[ ]], (( )) and let: judged as a command of that name.
		keyword := "[["
		switch x.(type) {
		case *syntax.ArithmCmd:
			keyword = "(("
		case *syntax.LetClause:
			keyword = "let"
		}
		cmd.words, cmd.dyn = []string{keyword}, []bool{false}
		cmd.raw = c.rawText(x, raw)
		cmd.masked = cmd.raw
	default:
		// A compound command, or redirections alone: its redirections
		// are judged as a command with no words.
		if len(s.Redirs) == 0 {
			return nil, inner
		}
		cmd.raw = strings.Join(raw, " ")
	}
	return cmd, inner
}

// rawText is a command's source and its redirections, with each
// backslash-newline removed as the shell removes it, so a pattern matched
// over it sees git \<newline>push as git push.
func (c *cmdCollector) rawText(n syntax.Node, redirs []string) string {
	return strings.ReplaceAll(strings.Join(append([]string{c.text(n)}, redirs...), " "), "\\\n", "")
}

// stdinBody is the text a heredoc or here-string gives.
func (c *cmdCollector) stdinBody(r *syntax.Redirect) string {
	if r.Op == syntax.WordHdoc {
		text, _ := c.word(r.Word)
		return text
	}
	if r.Hdoc == nil || len(r.Hdoc.Parts) == 0 {
		return ""
	}
	stop, _ := c.word(r.Word)
	body := heredocBody(c.src[r.Hdoc.Parts[0].Pos().Offset():r.Hdoc.End().Offset()], stop, r.Op == syntax.DashHdoc)
	if r.Op == syntax.DashHdoc {
		// <<- strips the leading tabs of every line.
		lines := strings.Split(body, "\n")
		for i, line := range lines {
			lines[i] = strings.TrimLeft(line, "\t")
		}
		body = strings.Join(lines, "\n")
	}
	return body
}

// redirect records a redirection on cmd and returns its source without a
// heredoc body.
func (c *cmdCollector) redirect(cmd *shellCmd, r *syntax.Redirect) string {
	raw := c.src[r.Pos().Offset():r.Word.End().Offset()]
	switch r.Op {
	case syntax.Hdoc, syntax.DashHdoc:
		body := ""
		if r.Hdoc != nil && len(r.Hdoc.Parts) > 0 {
			stop, _ := c.word(r.Word)
			body = heredocBody(c.src[r.Hdoc.Parts[0].Pos().Offset():r.Hdoc.End().Offset()], stop, r.Op == syntax.DashHdoc)
		}
		cmd.heredocs = append(cmd.heredocs, body)
		quoted := quotedWord(r.Word)
		if !quoted && r.Hdoc != nil {
			syntax.Walk(r.Hdoc, func(n syntax.Node) bool {
				switch n.(type) {
				case *syntax.CmdSubst:
					cmd.heredocExpands, cmd.heredocSubst = true, true
				case *syntax.ParamExp, *syntax.ArithmExp:
					cmd.heredocExpands = true
				}
				return true
			})
		}
		if !plainDelimiter(r.Word) || !quoted && r.Hdoc != nil && strings.Contains(c.text(r.Hdoc), "\\\n") {
			// The parser joins a backslash-newline in an unquoted body
			// before it looks for the delimiter line, and reads $'EOF' as
			// EOF; a shell may not.
			cmd.unsure = true
		}
	case syntax.WordHdoc:
		text, _ := c.word(r.Word)
		cmd.hereStrings = append(cmd.hereStrings, text)
	case syntax.DplIn, syntax.DplOut:
		// >&2 and <&- copy or close a descriptor and name no file.
		if text, dyn := c.word(r.Word); dyn || !isDigits(text) && text != "-" {
			cmd.redirs = append(cmd.redirs, text)
			cmd.redirDyn = append(cmd.redirDyn, dyn)
		}
	default:
		text, dyn := c.word(r.Word)
		cmd.redirs = append(cmd.redirs, text)
		cmd.redirDyn = append(cmd.redirDyn, dyn)
	}
	return raw
}

// heredocBody is a heredoc's source without its delimiter line, which the
// parser's positions include. With tabs, as for <<-, leading tabs do not
// count on the delimiter line.
func heredocBody(src, stop string, tabs bool) string {
	start := strings.LastIndexByte(src, '\n') + 1
	last := src[start:]
	if tabs {
		last = strings.TrimLeft(last, "\t")
	}
	if last == stop {
		return src[:start]
	}
	return src
}

// plainWord matches a heredoc delimiter the shells agree on.
var plainWord = regexp.MustCompile(`^[A-Za-z_][\w.-]*$`)

// plainDelimiter reports whether a heredoc delimiter is a plain word, quoted
// whole or not at all: EOF, 'EOF', "EOF" or \EOF.
func plainDelimiter(w *syntax.Word) bool {
	if len(w.Parts) != 1 {
		return false
	}
	switch p := w.Parts[0].(type) {
	case *syntax.Lit:
		return plainWord.MatchString(strings.TrimPrefix(p.Value, `\`))
	case *syntax.SglQuoted:
		return !p.Dollar && plainWord.MatchString(p.Value)
	case *syntax.DblQuoted:
		if len(p.Parts) != 1 || p.Dollar {
			return false
		}
		lit, ok := p.Parts[0].(*syntax.Lit)
		return ok && plainWord.MatchString(lit.Value)
	}
	return false
}

// quotedWord reports whether any part of a heredoc delimiter is quoted, so
// the shell expands nothing in the body.
func quotedWord(w *syntax.Word) bool {
	for _, p := range w.Parts {
		switch p := p.(type) {
		case *syntax.SglQuoted, *syntax.DblQuoted:
			return true
		case *syntax.Lit:
			if strings.Contains(p.Value, `\`) {
				return true
			}
		}
	}
	return false
}

// assign renders an assignment as NAME=value, and whether its value holds an
// expansion.
func (c *cmdCollector) assign(a *syntax.Assign) (string, bool) {
	name := ""
	if a.Name != nil {
		name = a.Name.Value
	}
	if a.Index != nil {
		// BASH_ALIASES[x]=...: the index stays as written.
		name += "[" + c.text(a.Index) + "]"
	}
	switch {
	case a.Value != nil:
		text, dyn := c.word(a.Value)
		return name + "=" + text, dyn
	case a.Array != nil:
		text := c.text(a.Array)
		return name + "=" + text, strings.ContainsAny(text, "$`")
	}
	return name, false
}

// word renders a word as the shell would pass it once it removes quotes,
// with expansions left as written, and whether it holds one: a parameter,
// command, arithmetic or process substitution, or a glob or brace that may
// reach a dot file such as .git.
func (c *cmdCollector) word(w *syntax.Word) (string, bool) {
	var b strings.Builder
	dyn := c.parts(&b, w.Parts, false)
	return b.String(), dyn || c.mayExpandToDotFile(w)
}

// parts renders word parts into b and reports whether any is an expansion.
func (c *cmdCollector) parts(b *strings.Builder, parts []syntax.WordPart, quoted bool) bool {
	dyn := false
	for _, p := range parts {
		switch p := p.(type) {
		case *syntax.Lit:
			b.WriteString(unescape(p.Value, quoted))
		case *syntax.SglQuoted:
			if !p.Dollar {
				b.WriteString(p.Value)
				break
			}
			// $'...' decodes its escapes.
			v, err := expand.Literal(nil, &syntax.Word{Parts: []syntax.WordPart{p}})
			if err != nil {
				v = c.text(p)
				dyn = true
			}
			b.WriteString(v)
		case *syntax.DblQuoted:
			if c.parts(b, p.Parts, true) {
				dyn = true
			}
		default:
			// $x, $(...), $((...)), <(...), an extended glob.
			b.WriteString(c.text(p))
			switch p.(type) {
			case *syntax.ParamExp, *syntax.CmdSubst, *syntax.ArithmExp, *syntax.ProcSubst, *syntax.ExtGlob:
				// An extended glob such as .@(git|x) can match a dot
				// file too.
				dyn = true
			}
		}
	}
	return dyn
}

// unescape removes the backslashes the shell removes from literal text:
// before any character outside quotes, and before $ ` " \ inside double
// quotes. On Windows a backslash outside quotes stays as a path separator
// unless it escapes a blank, a quote or an operator, as shellWords reads it.
func unescape(s string, quoted bool) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		next := s[i+1]
		if quoted && strings.IndexByte("$`\"\\", next) < 0 ||
			!quoted && backslashSeparates && strings.IndexByte(shellEscapable, next) < 0 {
			// The backslash stays; the next character is written as
			// itself on the next turn.
			b.WriteByte('\\')
			continue
		}
		i++
		b.WriteByte(s[i])
	}
	return b.String()
}

// mayExpandToDotFile reports whether an unquoted glob or brace in a word may
// expand to a protected path or a dot file such as .git: a brace list one of
// whose words is one, or a glob in a path part that starts with a dot, as in
// .g*/slopcop (a glob matches a leading dot only when one is written).
func (c *cmdCollector) mayExpandToDotFile(w *syntax.Word) bool {
	words := []*syntax.Word{w}
	split := &syntax.Word{Parts: slices.Clone(w.Parts)}
	if syntax.SplitBraces(split) {
		// A range ({1..9}) is not expanded: its size has no bound worth
		// paying for, so it counts as dynamic, as do lists that would
		// expand to too many words.
		if braceWords(split) > 256 {
			return true
		}
		words = expand.Braces(split)
	}
	for _, word := range words {
		var b strings.Builder
		c.parts(&b, word.Parts, false)
		text := b.String()
		if len(words) > 1 && protectedPath(text) {
			return true
		}
		if !slices.ContainsFunc(word.Parts, func(p syntax.WordPart) bool {
			lit, ok := p.(*syntax.Lit)
			return ok && strings.ContainsAny(lit.Value, "*?[")
		}) {
			continue
		}
		for _, part := range strings.Split(filepath.ToSlash(text), "/") {
			if strings.HasPrefix(part, ".") && strings.ContainsAny(part, "*?[") {
				return true
			}
		}
	}
	return false
}

// braceWords is how many words brace expansion makes of w, counting a
// range, or more than a few hundred words, as too many to expand.
func braceWords(w *syntax.Word) int {
	const tooMany = 257
	n := 1
	for _, p := range w.Parts {
		br, ok := p.(*syntax.BraceExp)
		if !ok {
			continue
		}
		if br.Sequence {
			return tooMany
		}
		sum := 0
		for _, elem := range br.Elems {
			sum += braceWords(elem)
			if sum >= tooMany {
				return tooMany
			}
		}
		n *= max(sum, 1)
		if n >= tooMany {
			return tooMany
		}
	}
	return n
}

// mask renders a word with the inside of each quoted part blanked, keeping
// the quotes: "my dir" becomes "".
func (c *cmdCollector) mask(w *syntax.Word) string {
	var b strings.Builder
	for _, p := range w.Parts {
		switch p := p.(type) {
		case *syntax.Lit:
			b.WriteString(p.Value)
		case *syntax.SglQuoted:
			b.WriteString("''")
		case *syntax.DblQuoted:
			b.WriteString(`""`)
		case *syntax.CmdSubst, *syntax.ProcSubst:
			// The commands inside are judged on their own.
			b.WriteString("$()")
		default:
			// ${x:-'text'}: quoted text in an expansion is masked too.
			b.WriteString(c.maskNested(p))
		}
	}
	return b.String()
}

// maskNested is a node's source with the inside of each quoted part in it
// blanked, and each command substitution in it shortened to $().
func (c *cmdCollector) maskNested(n syntax.Node) string {
	start, end := n.Pos().Offset(), n.End().Offset()
	var b strings.Builder
	at := start
	syntax.Walk(n, func(m syntax.Node) bool {
		var mask string
		switch m.(type) {
		case *syntax.SglQuoted:
			mask = "''"
		case *syntax.DblQuoted:
			mask = `""`
		case *syntax.CmdSubst, *syntax.ProcSubst:
			mask = "$()"
		default:
			return true
		}
		if m.Pos().Offset() >= at {
			b.WriteString(c.src[at:m.Pos().Offset()])
			b.WriteString(mask)
			at = m.End().Offset()
		}
		return false
	})
	b.WriteString(c.src[at:end])
	return b.String()
}

// sedScript is the script a sed argument patternArgs found gives: the
// argument itself, or the value glued to -e or --expression=.
func sedScript(arg string) string {
	switch {
	case strings.HasPrefix(arg, "--expression="):
		return strings.TrimPrefix(arg, "--expression=")
	case len(arg) > 1 && arg[0] == '-':
		if k := strings.IndexByte(arg, 'e'); k > 0 {
			return arg[k+1:]
		}
	}
	return arg
}

// sedRuns reports whether a sed script may run a command: GNU sed's e
// command, or the e flag of s with any delimiter (s/x/y/e, sX^XXe, s|^|| e).
// It reads the script command by command, and a script it cannot read counts
// as one that runs.
func sedRuns(script string) bool {
	s := script
	i := 0
	skipBlank := func() {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
	}
	// delimited skips text up to an unescaped delim and past it, and
	// reports whether it found one.
	delimited := func(delim byte) bool {
		for ; i < len(s); i++ {
			switch {
			case s[i] == '\\':
				i++
			case s[i] == delim:
				i++
				return true
			case s[i] == '\n' && delim != '\n':
				return false
			}
		}
		return false
	}
	// toNewline skips a comment or file name, which ends at the next
	// newline whatever comes before it.
	toNewline := func() {
		for i < len(s) && s[i] != '\n' {
			i++
		}
	}
	// toLineEnd skips the text of a, i or c, which a backslash continues
	// onto the next line.
	toLineEnd := func() {
		for i < len(s) && s[i] != '\n' {
			if s[i] == '\\' {
				i++
			}
			i++
		}
	}
	address := func() bool {
		switch {
		case i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '+' || s[i] == '~'):
			for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '+' || s[i] == '~') {
				i++
			}
		case i < len(s) && s[i] == '$':
			i++
		case i < len(s) && s[i] == '/':
			i++
			if !delimited('/') {
				return false
			}
		case i+1 < len(s) && s[i] == '\\':
			delim := s[i+1]
			i += 2
			if !delimited(delim) {
				return false
			}
		default:
			return true
		}
		for i < len(s) && (s[i] == 'I' || s[i] == 'M') {
			i++
		}
		return true
	}
	for i < len(s) {
		for i < len(s) && strings.IndexByte(" \t\n;", s[i]) >= 0 {
			i++
		}
		if i >= len(s) {
			return false
		}
		if !address() {
			return true
		}
		skipBlank()
		if i < len(s) && s[i] == ',' {
			i++
			skipBlank()
			if !address() {
				return true
			}
		}
		skipBlank()
		for i < len(s) && s[i] == '!' {
			i++
			skipBlank()
		}
		if i >= len(s) {
			return true
		}
		cmd := s[i]
		i++
		switch cmd {
		case '{', '}', '=', 'd', 'D', 'F', 'g', 'G', 'h', 'H', 'l', 'n', 'N', 'p', 'P', 'x', 'z':
		case 'q', 'Q', 'L':
			skipBlank()
			for i < len(s) && s[i] >= '0' && s[i] <= '9' {
				i++
			}
		case 'a', 'i', 'c':
			toLineEnd()
		case '#', 'r', 'R', 'w', 'W':
			toNewline()
		case ':', 'b', 't', 'T', 'v':
			for i < len(s) && strings.IndexByte(";\n", s[i]) < 0 {
				i++
			}
		case 'y':
			if i >= len(s) {
				return true
			}
			delim := s[i]
			i++
			if !delimited(delim) || !delimited(delim) {
				return true
			}
		case 's':
			if i >= len(s) || s[i] == '\\' || s[i] == '\n' {
				return true
			}
			delim := s[i]
			i++
			if !delimited(delim) || !delimited(delim) {
				return true
			}
			// Flags, blanks between them taken as a lenient sed does; w
			// takes the rest of the line as a file name.
			for i < len(s) && strings.IndexByte(";\n}", s[i]) < 0 {
				switch c := s[i]; {
				case c == 'e':
					return true
				case c == 'w':
					toNewline()
				case c == ' ' || c == '\t' || c == 'g' || c == 'p' || c == 'i' || c == 'I' || c == 'm' || c == 'M' || c >= '0' && c <= '9':
					i++
				default:
					return true
				}
			}
		default:
			// e, or a command this reader does not know.
			return true
		}
	}
	return false
}
