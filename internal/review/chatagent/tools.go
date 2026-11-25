package chatagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	acp "github.com/coder/acp-go-sdk"

	"github.com/BrokkAi/slopcop/internal/gitx"
)

const (
	// MaxFile is the largest file a reviewer can read. A change to a larger
	// one fails the reviewer before it starts.
	MaxFile = 1 << 20
	// maxToolResult bounds one tool result.
	maxToolResult = 64 << 10
	// maxReadLine is the most of one line a read returns at a time.
	maxReadLine = maxToolResult / 2
	// defaultReadLines is how much of a file one read returns by default.
	defaultReadLines = 400
	// maxSearchLine shortens matches on very long lines.
	maxSearchLine = 300
	// searchTimeout bounds one search, whatever its pattern.
	searchTimeout = 30 * time.Second
)

// snapshot answers tool calls from one git tree. Paths are only keys into the
// tree's listing: no tool opens a path on disk, so a symlink or a ".." in the
// tree cannot reach anything outside it.
type snapshot struct {
	root, tree, gitDir, base string
	files                    map[string]gitx.TreeFile
	paths                    []string
	// changed holds the files the change adds or edits, diffPaths also includes
	// deleted paths between base and tree, unread holds changed files whose
	// last read_file failed, and unreadDiff holds diffPaths whose last read_diff
	// failed (which a subsequent read_file cannot clear).
	changed, diffPaths, unread, unreadDiff map[string]bool
}

// errPastEnd marks a read that starts after a file's last line. It is not a
// failure to read the file.
var errPastEnd = errors.New("past its end")

// loadSnapshot lists tree. With base, it also notes which files changed.
func loadSnapshot(ctx context.Context, root, tree, base string) (*snapshot, error) {
	files, err := gitx.TreeFiles(ctx, root, tree, MaxFile)
	if err != nil {
		return nil, err
	}
	gitDir, err := gitx.RunContext(ctx, root, nil, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, err
	}
	s := &snapshot{base: base, root: root, tree: tree, gitDir: gitDir, files: make(map[string]gitx.TreeFile, len(files)), changed: map[string]bool{}, diffPaths: map[string]bool{}, unread: map[string]bool{}, unreadDiff: map[string]bool{}}
	if base != "" {
		changed, err := gitx.ChangedFiles(ctx, root, base, tree)
		if err != nil {
			return nil, err
		}
		for _, p := range changed {
			s.changed[p] = true
			s.diffPaths[p] = true
		}
		raw, err := gitx.RunWithoutRepoAttributes(root, "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--ignore-submodules=none", "--name-only", "-z", base, tree)
		if err != nil {
			return nil, err
		}
		for _, entry := range bytes.Split(raw, []byte{0}) {
			if len(entry) > 0 {
				s.diffPaths[string(entry)] = true
			}
		}
	}
	for _, f := range files {
		s.files[f.Path] = f
		s.paths = append(s.paths, f.Path)
	}
	sort.Strings(s.paths)
	return s, nil
}

// toolDefinitions are the only tools the model is offered.
func toolDefinitions() []map[string]any {
	tool := func(name, description string, properties map[string]any, required ...string) map[string]any {
		if required == nil {
			required = []string{}
		}
		return map[string]any{"type": "function", "function": map[string]any{
			"name": name, "description": description,
			"parameters": map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false},
		}}
	}
	text := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	number := func(description string) map[string]any {
		return map[string]any{"type": "integer", "description": description}
	}
	return []map[string]any{
		tool("list_changes", "List all changed and deleted files in the captured diff (git diff --numstat), optionally filtered by path prefix and paginated by line.", map[string]any{
			"prefix": text("Only paths starting with this."), "offset": number("First line to read, from 1."), "limit": number("Number of lines to read."),
		}),
		tool("read_diff", "Read a captured per-file diff, including deleted files and readable binary text.", map[string]any{"path": text("Path from repository root."), "offset": number("First line from 1."), "limit": number("Number of lines."), "column": number("Byte column for long lines.")}, "path"),
		tool("read_file", "Read a file of the reviewed snapshot, with line numbers.", map[string]any{
			"path": text("Path from the repository root."), "offset": number("First line to read, from 1."), "limit": number("Number of lines to read."),
			"column": number("Byte position in the first line to start from, from 1, to continue a very long line."),
		}, "path"),
		tool("list_files", "List the snapshot's files with their sizes.", map[string]any{"prefix": text("Only paths starting with this.")}),
		tool("search_files", "Search the snapshot's text files with an extended regular expression.", map[string]any{
			"pattern": text("Extended regular expression."), "prefix": text("Only paths under this directory or file."),
		}, "pattern"),
	}
}

// toolKind is the ACP kind reported for a tool, which SlopCop's guard checks,
// and whether the agent offers the tool.
func toolKind(name string) (acp.ToolKind, bool) {
	switch name {
	case "read_file", "read_diff":
		return acp.ToolKindRead, true
	case "list_changes", "list_files", "search_files":
		return acp.ToolKindSearch, true
	}
	return acp.ToolKindOther, false
}

// call runs one tool. Its error is sent to the model, which may recover.
func (s *snapshot) call(ctx context.Context, name, arguments string) (string, error) {
	var in struct {
		Path    string `json:"path"`
		Offset  int    `json:"offset"`
		Limit   int    `json:"limit"`
		Column  int    `json:"column"`
		Prefix  string `json:"prefix"`
		Pattern string `json:"pattern"`
	}
	if strings.TrimSpace(arguments) != "" {
		if err := json.Unmarshal([]byte(arguments), &in); err != nil {
			return "", errors.New("the tool's arguments are not valid JSON")
		}
	}
	switch name {
	case "list_changes":
		return s.listChanges(ctx, cleanPath(in.Prefix), in.Offset, in.Limit)
	case "read_diff":
		if in.Path == "" {
			return "", errors.New("path is required")
		}
		path := cleanPath(in.Path)
		data, err := gitx.FilePatch(ctx, s.root, s.base, s.tree, path)
		var out string
		if err == nil {
			out, err = renderRead(data, path, in.Offset, in.Limit, in.Column)
		}
		if s.diffPaths[path] {
			if err == nil || errors.Is(err, errPastEnd) {
				delete(s.unread, path)
				delete(s.unreadDiff, path)
			} else {
				s.unreadDiff[path] = true
			}
		}
		return out, err
	case "read_file":
		return s.read(ctx, cleanPath(in.Path), in.Offset, in.Limit, in.Column)
	case "list_files":
		return s.list(cleanPath(in.Prefix)), nil
	case "search_files":
		return s.search(ctx, in.Pattern, cleanPath(in.Prefix))
	}
	return "", fmt.Errorf("there is no tool named %q; use list_changes, read_diff, read_file, list_files or search_files", name)
}

// listChanges returns the deterministic numstat of all changed and deleted
// files between base and tree, optionally filtered by prefix and paginated.
func (s *snapshot) listChanges(ctx context.Context, prefix string, offset, limit int) (string, error) {
	if s.base == "" {
		return "(no base tree recorded)", nil
	}
	raw, err := gitx.RunWithoutRepoAttributes(s.root, "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--ignore-submodules=none", "--numstat", "-z", s.base, s.tree)
	if err != nil {
		return "", err
	}
	var lines []string
	for _, entry := range bytes.Split(raw, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		parts := strings.SplitN(string(entry), "\t", 3)
		if len(parts) != 3 {
			continue
		}
		if prefix != "" && !strings.HasPrefix(parts[2], prefix) {
			continue
		}
		path := parts[2]
		if strings.ContainsAny(path, "\t\r\n") {
			path = strconv.Quote(path)
		}
		lines = append(lines, parts[0]+"\t"+parts[1]+"\t"+path)
	}
	if len(lines) == 0 {
		return "(no matching changed files)", nil
	}
	data := []byte(strings.Join(lines, "\n") + "\n")
	return renderRead(data, "changed files", offset, limit, 0)
}

func cleanPath(p string) string {
	return strings.TrimPrefix(p, "./")
}

// IsBinary reports whether read_file refuses this content, by git's own test:
// a NUL in the first 8000 bytes.
func IsBinary(data []byte) bool {
	return bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0
}

// unreadChange names a changed file whose last read or diff failed, or returns "".
func (s *snapshot) unreadChange() string {
	for _, p := range s.paths {
		if s.unread[p] || s.unreadDiff[p] {
			return p
		}
	}
	var extra []string
	for p := range s.unread {
		extra = append(extra, p)
	}
	for p := range s.unreadDiff {
		extra = append(extra, p)
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		return extra[0]
	}
	return ""
}

// read reads a file and records whether a changed file could be read.
func (s *snapshot) read(ctx context.Context, path string, offset, limit, column int) (string, error) {
	out, err := s.readFile(ctx, path, offset, limit, column)
	if s.changed[path] {
		if err == nil {
			delete(s.unread, path)
		} else if !errors.Is(err, errPastEnd) {
			s.unread[path] = true
		}
	}
	return out, err
}

func (s *snapshot) readFile(ctx context.Context, path string, offset, limit, column int) (string, error) {
	f, ok := s.files[path]
	if !ok {
		return "", fmt.Errorf("%q is not a readable file of the snapshot (files over 1 MiB, symlinks and submodules are not)", path)
	}
	data, err := gitx.Blob(ctx, s.root, f.Object)
	if err != nil {
		return "", errors.New("the file could not be read from the snapshot")
	}
	if IsBinary(data) {
		return "", fmt.Errorf("%q is a binary file", path)
	}
	return renderRead(data, path, offset, limit, column)
}

func renderRead(data []byte, path string, offset, limit, column int) (string, error) {
	if offset < 1 {
		offset = 1
	}
	if limit < 1 {
		limit = defaultReadLines
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if offset > len(lines) {
		return "", fmt.Errorf("%q has %d lines, so offset %d is %w", path, len(lines), offset, errPastEnd)
	}
	var b strings.Builder
	end := min(len(lines), offset-1+limit)
	for i := offset - 1; i < end; i++ {
		text := lines[i]
		start := 0
		if i == offset-1 && column > 1 {
			start = min(column-1, len(text))
			text = text[start:]
		}
		if len(text) > maxReadLine {
			// A long line comes in pieces, each from the start of a result.
			if b.Len() > 0 {
				end = i
				break
			}
			cut := maxReadLine
			for cut > 0 && !utf8.RuneStart(text[cut]) {
				cut--
			}
			fmt.Fprintf(&b, "%d\t%s\n[line %d continues; read it with offset %d and column %d]\n", i+1, text[:cut], i+1, i+1, start+cut+1)
			return b.String(), nil
		}
		line := fmt.Sprintf("%d\t%s\n", i+1, text)
		if b.Len()+len(line) > maxToolResult {
			end = i
			break
		}
		b.WriteString(line)
	}
	if end < len(lines) {
		fmt.Fprintf(&b, "[%d of %d lines shown; continue with offset %d]\n", end, len(lines), end+1)
	}
	return b.String(), nil
}

func (s *snapshot) list(prefix string) string {
	var b strings.Builder
	for _, p := range s.paths {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		line := fmt.Sprintf("%s\t%d\n", p, s.files[p].Size)
		if b.Len()+len(line) > maxToolResult {
			b.WriteString("[listing cut; narrow it with prefix]\n")
			break
		}
		b.WriteString(line)
	}
	if b.Len() == 0 {
		return "No files match.\n"
	}
	return b.String()
}

// search runs git grep over the tree's blobs. It runs no textconv filter and
// stops at the size of one tool result. Like the review diff, it runs from an
// empty work tree with attr.tree cleared, so the repository's .gitattributes
// cannot mark a text file binary and keep it out of the results.
func (s *snapshot) search(ctx context.Context, pattern, prefix string) (string, error) {
	if pattern == "" {
		return "", errors.New("pattern is required")
	}
	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()
	empty, err := os.MkdirTemp("", "slopcop-search-")
	if err != nil {
		return "", errors.New("the search could not start")
	}
	defer os.RemoveAll(empty)
	args := []string{"--git-dir=" + s.gitDir, "--work-tree=" + empty, "-c", "attr.tree=", "grep", "--no-color", "--no-textconv", "-I", "-n", "-E", "-e", pattern, s.tree, "--"}
	if prefix != "" {
		args = append(args, ":(top,literal)"+strings.TrimSuffix(prefix, "/"))
	}
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = empty
	command.Env = append(slices.DeleteFunc(os.Environ(), func(entry string) bool {
		name, _, _ := strings.Cut(entry, "=")
		return name == "GIT_ATTR_SOURCE" || name == "GIT_INDEX_FILE" || name == "GIT_DIR" || name == "GIT_WORK_TREE"
	}), "GIT_INDEX_FILE="+filepath.Join(empty, "index"))
	out := &cappedOutput{limit: maxToolResult * 4, full: cancel}
	var stderr bytes.Buffer
	command.Stdout, command.Stderr = out, &stderr
	err = command.Run()
	var exit *exec.ExitError
	switch {
	case out.cut:
	case ctx.Err() != nil:
		return "", errors.New("the search took too long; use a simpler pattern or a narrower prefix")
	case errors.As(err, &exit) && exit.ExitCode() == 1 && stderr.Len() == 0:
		return "No matches.\n", nil
	case err != nil:
		return "", errors.New("the search failed; check the pattern")
	}
	var b strings.Builder
	cut := out.cut
	for _, line := range strings.Split(strings.TrimSuffix(out.b.String(), "\n"), "\n") {
		line = strings.TrimPrefix(line, s.tree+":")
		if len(line) > maxSearchLine {
			line = line[:maxSearchLine] + "...[line cut]"
		}
		if b.Len()+len(line)+1 > maxToolResult {
			cut = true
			break
		}
		b.WriteString(line + "\n")
	}
	if cut {
		b.WriteString("[results cut; narrow the pattern or prefix]\n")
	}
	return b.String(), nil
}

// cappedOutput keeps the first limit bytes and then ends the command.
type cappedOutput struct {
	b     bytes.Buffer
	limit int
	cut   bool
	full  func()
}

func (c *cappedOutput) Write(p []byte) (int, error) {
	if room := c.limit - c.b.Len(); room < len(p) {
		c.b.Write(p[:max(room, 0)])
		if !c.cut {
			c.cut = true
			c.full()
		}
		return len(p), nil
	}
	return c.b.Write(p)
}
