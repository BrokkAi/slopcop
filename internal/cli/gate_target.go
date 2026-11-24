package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
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
// be read without running the shell ($VAR, $(...), cd -, popd), the gate
// cannot tell what would be pushed and blocks, asking for a push it can
// read. Like pushCommand, this guards against an agent's slip; it is not a
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

// redirection matches the redirections whose & is not an operator: 2>&1,
// >&2, <&0, >&-, >&file, &> and &>>. A |& pipes stderr too, and stays a pipe.
var redirection = regexp.MustCompile(`[0-9]*[<>]&(?:[0-9]+-?|-)?|&>>?|\|&`)

// segments splits a command line at the operators that end a command: &&,
// ||, ;, &, |, newlines, subshells and command substitution. Quoted text and
// escaped characters are not operators, and a backslash-newline continues
// the line. Redirections are rewritten first, so `cd dir 2>&1 && …` is not
// read as a background job and `cd dir >&file` keeps its file a
// redirection's.
func segments(command string) []segment {
	command = strings.ReplaceAll(command, "\\\n", "  ")
	command = redirection.ReplaceAllStringFunc(command, func(match string) string {
		switch {
		case match == "|&":
			return "| "
		case strings.HasPrefix(match, "&"):
			// &> and &>> name a file next.
			return " " + match[1:]
		case strings.HasSuffix(match, "&"):
			// >&file names a file next, as > does.
			return match[:len(match)-1] + " "
		default:
			// A descriptor copy or close names no file.
			return strings.Repeat(" ", len(match))
		}
	})
	var parts []segment
	start := 0
	var quote byte
	for i := 0; i < len(command); i++ {
		c := command[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
			continue
		case quote == '"':
			if c == '\\' {
				i++
			} else if c == '"' {
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
		op := ""
		switch {
		case strings.HasPrefix(command[i:], "&&"), strings.HasPrefix(command[i:], "||"):
			op = command[i : i+2]
		case strings.IndexByte(";&|\n()`", c) >= 0:
			op = command[i : i+1]
		default:
			continue
		}
		parts = append(parts, segment{text: command[start:i], next: op})
		i += len(op) - 1
		start = i + 1
	}
	return append(parts, segment{text: command[start:]})
}

// shellWords splits a command into words as the shell does before
// expansion: quotes and backslashes group and are removed, so `my\ repo` and
// "my repo" are one word.
func shellWords(text string) []string {
	var words []string
	var word strings.Builder
	inWord := false
	var quote byte
	for i := 0; i < len(text); i++ {
		c := text[i]
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
			i++
			word.WriteByte(text[i])
			inWord = true
		case c == '\'' || c == '"':
			quote = c
			inWord = true
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if inWord {
				words = append(words, word.String())
				word.Reset()
				inWord = false
			}
		default:
			word.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		words = append(words, word.String())
	}
	return words
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

// fileRedirect matches a redirection to or from a file at the start of a
// word, such as >, 2>> or <.
var fileRedirect = regexp.MustCompile(`^[0-9]*(?:>>?|<)`)

// commandPrefixes are words that can stand before a command in the same
// shell: keywords of compound commands and the builtins that run another.
var commandPrefixes = map[string]bool{
	"builtin": true, "command": true, "{": true, "!": true, "time": true,
	"if": true, "then": true, "else": true, "elif": true, "do": true, "while": true, "until": true,
}

// cdTarget is the directory a cd, pushd or popd command moves to, and
// whether the segment is one. Redirections are not arguments. The target is
// unknownDir where it cannot be read.
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
	var args []string
	for i := 1; i < len(words); i++ {
		word := words[i]
		if op := fileRedirect.FindString(word); op != "" {
			// > /dev/null takes the next word; >/dev/null holds its own.
			if op == word {
				i++
			}
			continue
		}
		args = append(args, word)
	}
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

// pushTargets lists the directories the pushes on a command line may run in,
// starting from cwd, and whether some push may run in a directory it cannot
// read. Where the shell could run a push in more than one directory, it
// lists each: after `cd dir && make || <push>`, the push runs in the old
// directory when the cd failed and in dir when make did.
func pushTargets(cwd, command string) ([]string, bool) {
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
	unsure := false
	seen := map[string]bool{}
	add := func(target string) {
		if target == unknownDir {
			unsure = true
		} else if !seen[target] {
			seen[target] = true
			targets = append(targets, target)
		}
	}
	prev := ""
	for _, seg := range segments(command) {
		// A pipeline runs each of its commands in a subshell.
		piped := prev == "|" || seg.next == "|"
		prev = seg.next
		words := shellWords(seg.text)
		switch {
		case gitPush.MatchString(seg.text):
			for _, dir := range dirs {
				add(gitDir(dir, words))
			}
		case ghPRCreate.MatchString(seg.text):
			for _, dir := range dirs {
				add(dir)
			}
		default:
			if moved, ok := cdDirs(dirs, words, seg.next, branched, piped); ok {
				dirs = moved
			}
		}
		been = union(been, dirs)
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
			// ;, a newline, & or |: a new list starts here, wherever the
			// last one stopped.
			if length > 1 {
				dirs = been
			}
			been, branched, length = dirs, false, 0
		}
	}
	return targets, unsure
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
	var moved []string
	exists, unknown := true, false
	for _, dir := range dirs {
		target, ok := cdTarget(dir, words)
		if !ok {
			return nil, false
		}
		if target == unknownDir {
			unknown = true
		} else if info, err := os.Stat(target); err != nil || !info.IsDir() {
			exists = false
		}
		moved = union(moved, []string{target})
	}
	switch {
	case branched || next == "||":
		return union(dirs, moved), true
	case next == "&&":
		return moved, true
	case unknown:
		return union(dirs, moved), true
	case exists:
		return moved, true
	default:
		// The cd fails, so the commands after ; run where they were.
		return nil, false
	}
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
