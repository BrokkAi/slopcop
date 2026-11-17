// Package reposize finds what takes up the space in a repository the check
// service says is near, or over, its checkout size limit. The service names no
// paths, so the client measures the base commit's tree itself, leaving out
// what .bifrostignore and the [check] exclude list of .slopcop/config.toml
// already keep out of every check, judged as the service's runner judges them.
package reposize

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// The service's checkout bounds, for a refusal that does not carry them.
const (
	DefaultMaxFiles = 50_000
	DefaultMaxBytes = 512 << 20
)

// Ranking keys.
const (
	RankedByBytes = "bytes"
	RankedByFiles = "files"
)

// configPath is the file whose [check] section excludes paths from checks.
const configPath = ".slopcop/config.toml"

// maxConfigBytes is the most of configPath the runner reads.
const maxConfigBytes = 64 << 10

// listed is how many entries a breakdown names.
const listed = 5

// dominant is the share of a top-level directory's size one child must hold
// for the breakdown to name the child instead: a narrower exclude that
// still frees most of the space.
const dominant = 0.8

// Breakdown is where the space goes in the base commit's tree once the
// repository's own exclusions are left out.
type Breakdown struct {
	// Base is the commit measured.
	Base string `json:"base"`
	// Files and Bytes count the tree's files the exclusions leave in. The
	// service also counts the fetched history, so its totals are higher.
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
	// ExcludedFiles and ExcludedBytes count what .bifrostignore and [check]
	// exclude already leave out.
	ExcludedFiles int   `json:"excludedFiles"`
	ExcludedBytes int64 `json:"excludedBytes"`
	// RankedBy is the bound Largest is ordered by: the one the repository is
	// closer to.
	RankedBy string `json:"rankedBy"`
	// Largest names the biggest top-level directories or files, or the one
	// child that holds most of one, most first.
	Largest []Entry `json:"largest"`
	// Exclude is the [check] exclude list the working tree's
	// .slopcop/config.toml already has, and HasCheck whether it has a
	// [check] table at all.
	Exclude  []string `json:"exclude,omitempty"`
	HasCheck bool     `json:"hasCheck,omitempty"`
	// ConfigError says .slopcop/config.toml did not parse, so its [check]
	// exclusions were not left out.
	ConfigError string `json:"configError,omitempty"`
}

// Entry is one directory or file and what it holds.
type Entry struct {
	Path      string `json:"path"`
	Directory bool   `json:"directory"`
	Files     int    `json:"files"`
	Bytes     int64  `json:"bytes"`
	// Pattern is the [check] exclude line that would leave it out.
	Pattern string `json:"pattern"`
}

// Usage is how full the checkout is, as the service measured it or, for a
// refusal, as the local tree comes to.
type Usage struct {
	Files, MaxFiles int64
	Bytes, MaxBytes int64
}

// Compute measures the tree of base in the repository at root. usage, when
// the service answered it, decides which bound Largest is ranked by.
func Compute(ctx context.Context, root, base string, usage *Usage) (*Breakdown, error) {
	files, err := listTree(ctx, root, base)
	if err != nil {
		return nil, err
	}
	breakdown := &Breakdown{Base: base, Largest: []Entry{}}
	check, err := readCheckConfig(root)
	if err != nil {
		breakdown.ConfigError = err.Error()
	} else if check != nil {
		breakdown.HasCheck = true
		breakdown.Exclude = check.Exclude
	}
	paths := make([]string, len(files))
	for i, file := range files {
		paths[i] = file.path
	}
	ignoreFiles, err := bifrostignoreFiles(ctx, root, paths)
	if err != nil {
		return nil, err
	}
	excluded, err := excludedPaths(ctx, paths, breakdown.Exclude, ignoreFiles)
	if err != nil {
		return nil, err
	}
	kept := files[:0:0]
	for _, file := range files {
		if excluded[file.path] {
			breakdown.ExcludedFiles++
			breakdown.ExcludedBytes += file.bytes
			continue
		}
		breakdown.Files++
		breakdown.Bytes += file.bytes
		kept = append(kept, file)
	}
	if usage == nil {
		usage = &Usage{Files: int64(breakdown.Files), Bytes: breakdown.Bytes}
	}
	breakdown.RankedBy = rankedBy(*usage)
	breakdown.Largest = largest(kept, breakdown.RankedBy)
	return breakdown, nil
}

// rankedBy is the bound the usage is closer to.
func rankedBy(usage Usage) string {
	maxFiles, maxBytes := usage.MaxFiles, usage.MaxBytes
	if maxFiles <= 0 {
		maxFiles = DefaultMaxFiles
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if float64(usage.Files)/float64(maxFiles) > float64(usage.Bytes)/float64(maxBytes) {
		return RankedByFiles
	}
	return RankedByBytes
}

type treeFile struct {
	path  string
	bytes int64
}

// listTree lists every blob in base's tree with its size. Submodules are
// commits, not blobs, and the service does not fetch them.
func listTree(ctx context.Context, root, base string) ([]treeFile, error) {
	command := exec.CommandContext(ctx, "git", "-C", root, "ls-tree", "-r", "-l", "-z", "--full-tree", base)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-tree %s: %s", base, firstNonEmpty(strings.TrimSpace(stderr.String()), err.Error()))
	}
	var files []treeFile
	for _, record := range bytes.Split(output, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		meta, name, ok := bytes.Cut(record, []byte{'\t'})
		if !ok {
			return nil, fmt.Errorf("git ls-tree %s: unexpected entry %q", base, record)
		}
		fields := strings.Fields(string(meta))
		if len(fields) != 4 || fields[1] != "blob" {
			continue
		}
		size, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil {
			continue
		}
		files = append(files, treeFile{path: string(name), bytes: size})
	}
	return files, nil
}

// checkSection is the part of [check] that keeps paths from every rule.
type checkSection struct {
	Exclude []string `toml:"exclude"`
}

// readCheckConfig reads [check] from the working tree's root
// .slopcop/config.toml, the file a check of the working tree reads. A
// missing file, a symlink (the runner never follows one) or a file without
// [check] is nil.
func readCheckConfig(root string) (*checkSection, error) {
	full := filepath.Join(root, filepath.FromSlash(configPath))
	info, err := os.Lstat(full)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && !info.Mode().IsRegular()) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Size() > maxConfigBytes {
		return nil, fmt.Errorf("%s is over %d KiB", configPath, maxConfigBytes>>10)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, err
	}
	return parseCheckConfig(data)
}

// parseCheckConfig reads [check] from a .slopcop/config.toml's content; one
// without [check] is nil.
func parseCheckConfig(data []byte) (*checkSection, error) {
	var document struct {
		Check *checkSection `toml:"check"`
	}
	if _, err := toml.Decode(string(data), &document); err != nil {
		return nil, fmt.Errorf("%s does not parse: %v", configPath, err)
	}
	return document.Check, nil
}

// ExcludedPaths is the set of paths, among those given, that the
// .bifrostignore files and the [check] exclude list of .slopcop/config.toml
// in tree keep out of every check, so a check of tree need not send them.
// They are read from tree, the change the service checks, never from the
// disk, where an ignored or skip-worktree file can differ. The files that
// steer the check are never in the set, since the service reads them from
// the change: the .bifrostignore files and everything under .slopcop/ and
// .bifrost/, such as the config, policies and suppressions. Only a [check]
// the service would read the same way counts: see strictExclude.
func ExcludedPaths(ctx context.Context, root, tree string, paths []string) (map[string]bool, error) {
	config, ignoreFiles, err := treeExclusionFiles(ctx, root, tree)
	if err != nil {
		return nil, err
	}
	excluded, err := excludedPaths(ctx, paths, strictExclude(config), ignoreFiles)
	if err != nil {
		return nil, err
	}
	for name := range excluded {
		top, _, _ := strings.Cut(name, "/")
		if top == ".slopcop" || top == ".bifrost" || path.Base(name) == ".bifrostignore" {
			delete(excluded, name)
		}
	}
	return excluded, nil
}

// strictExclude is the [check] exclude list of a .slopcop/config.toml's
// content, read only where the service would read the same list: keys match
// exactly, as in the service's parser, and [check] holds nothing but exclude,
// a list of strings, and exclude_rules. Anything else, or a file over the
// size the runner reads, is nil, so the service, which sees the whole change,
// decides and reports.
func strictExclude(config []byte) []string {
	if len(config) == 0 || len(config) > maxConfigBytes {
		return nil
	}
	var document map[string]any
	if _, err := toml.Decode(string(config), &document); err != nil {
		return nil
	}
	check, ok := document["check"].(map[string]any)
	if !ok {
		return nil
	}
	var exclude []string
	for key, value := range check {
		switch key {
		case "exclude_rules":
		case "exclude":
			list, ok := value.([]any)
			if !ok {
				return nil
			}
			for _, item := range list {
				pattern, ok := item.(string)
				if !ok {
					return nil
				}
				exclude = append(exclude, pattern)
			}
		default:
			return nil
		}
	}
	return exclude
}

// treeExclusionFiles reads tree's root .slopcop/config.toml, nil when there
// is none, and its .bifrostignore files keyed by directory ("." for the
// root). Only regular files count: the runner never follows a symlink.
func treeExclusionFiles(ctx context.Context, root, tree string) ([]byte, map[string][]byte, error) {
	listing, err := gitOutput(ctx, root, "ls-tree", "-r", "-z", "--full-tree", tree)
	if err != nil {
		return nil, nil, err
	}
	var config []byte
	ignoreFiles := map[string][]byte{}
	for _, entry := range bytes.Split(listing, []byte{0}) {
		// Each entry is "<mode> <type> <object>\t<path>".
		meta, name, ok := bytes.Cut(entry, []byte{'\t'})
		fields := strings.Fields(string(meta))
		if !ok || len(fields) != 3 || (fields[0] != "100644" && fields[0] != "100755") {
			continue
		}
		file := string(name)
		if file != configPath && path.Base(file) != ".bifrostignore" {
			continue
		}
		content, err := gitOutput(ctx, root, "cat-file", "blob", fields[2])
		if err != nil {
			return nil, nil, err
		}
		if file == configPath {
			config = content
		} else {
			ignoreFiles[path.Dir(file)] = content
		}
	}
	return config, ignoreFiles, nil
}

// gitOutput runs git in the repository at root and returns its standard
// output.
func gitOutput(ctx context.Context, root string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %s", args[0], firstNonEmpty(strings.TrimSpace(stderr.String()), err.Error()))
	}
	return output, nil
}

// excludedPaths is the set of paths the .bifrostignore files, keyed by their
// directory, or the [check] exclude patterns leave out. Each is judged as the runner judges it: with
// `git check-ignore --no-index` in an empty repository, where every
// .bifrostignore is a .gitignore in its own directory, and, separately, the
// exclude patterns are the root .gitignore.
func excludedPaths(ctx context.Context, paths, exclude []string, ignoreFiles map[string][]byte) (map[string]bool, error) {
	excluded := map[string]bool{}
	if len(ignoreFiles) > 0 {
		ignored, err := gitIgnored(ctx, paths, ignoreFiles)
		if err != nil {
			return nil, err
		}
		for _, path := range ignored {
			excluded[path] = true
		}
	}
	if len(exclude) > 0 {
		lines := strings.Join(exclude, "\n") + "\n"
		ignored, err := gitIgnored(ctx, paths, map[string][]byte{".": []byte(lines)})
		if err != nil {
			return nil, err
		}
		for _, path := range ignored {
			excluded[path] = true
		}
	}
	return excluded, nil
}

// bifrostignoreFiles reads each .bifrostignore of the change as the working
// tree has it, keyed by its directory ("." for the root): the base tree's
// and new untracked ones. One the working tree deleted, or made a symlink,
// is not read, as the runner does not read it from the changed tree.
func bifrostignoreFiles(ctx context.Context, root string, tracked []string) (map[string][]byte, error) {
	candidates := map[string]bool{}
	for _, file := range tracked {
		if path.Base(file) == ".bifrostignore" {
			candidates[file] = true
		}
	}
	command := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "-z", "--others", "--exclude-standard", "--", ".bifrostignore", ":(glob)**/.bifrostignore")
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %v", err)
	}
	for _, file := range bytes.Split(output, []byte{0}) {
		if len(file) > 0 {
			candidates[string(file)] = true
		}
	}
	files := map[string][]byte{}
	for file := range candidates {
		full := filepath.Join(root, filepath.FromSlash(file))
		info, err := os.Lstat(full)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		content, err := os.ReadFile(full)
		if err != nil {
			return nil, err
		}
		files[path.Dir(file)] = content
	}
	return files, nil
}

// gitIgnored is the paths that the ignore files, keyed by directory ("." for
// the root) and written as that directory's .gitignore in a new, empty
// repository, exclude, as `git check-ignore --no-index` judges them. Git
// reads no user or system configuration there, so a global excludes file
// cannot change the answer, and patterns match case-sensitively, as on the
// runner. A case-insensitive disk, as on macOS, would still let data/'s file
// answer for Data/, so a path counts only when git names a file written for
// its own spelling. When two directories' names differ only in case, one
// file would stand for both, and leaving either out could drop a negation
// another file's pattern then overrides, so nothing counts as ignored.
func gitIgnored(ctx context.Context, paths []string, files map[string][]byte) ([]string, error) {
	directory, err := os.MkdirTemp("", "slopcop-reposize-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(directory)
	repository := filepath.Join(directory, "mirror")
	empty := filepath.Join(directory, "empty")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		return nil, err
	}
	env := isolatedEnv(empty)
	initialize := exec.CommandContext(ctx, "git", "init", "--quiet", repository)
	initialize.Env = env
	if output, err := initialize.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git init: %s", firstNonEmpty(strings.TrimSpace(string(output)), err.Error()))
	}
	folded := map[string]bool{}
	for dir := range files {
		if folded[strings.ToLower(dir)] {
			return nil, nil
		}
		folded[strings.ToLower(dir)] = true
	}
	sources := map[string]bool{}
	for dir, content := range files {
		target := filepath.Join(repository, filepath.FromSlash(dir))
		if err := os.MkdirAll(target, 0o755); err != nil {
			return nil, fmt.Errorf("mirror ignore files: %w", err)
		}
		if err := os.WriteFile(filepath.Join(target, ".gitignore"), content, 0o644); err != nil {
			return nil, fmt.Errorf("mirror ignore files: %w", err)
		}
		sources[path.Join(dir, ".gitignore")] = true
	}
	var input bytes.Buffer
	for _, file := range paths {
		input.WriteString(file)
		input.WriteByte(0)
	}
	check := exec.CommandContext(ctx, "git", "-C", repository, "-c", "core.excludesFile="+empty, "-c", "core.ignoreCase=false", "check-ignore", "--no-index", "--verbose", "-z", "--stdin")
	check.Env = env
	check.Stdin = &input
	var stderr bytes.Buffer
	check.Stderr = &stderr
	output, err := check.Output()
	var exit *exec.ExitError
	// 0 when some path is ignored, 1 when none is.
	if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1) {
		return nil, fmt.Errorf("git check-ignore: %s", firstNonEmpty(strings.TrimSpace(stderr.String()), err.Error()))
	}
	// Each match is four fields: source, line, pattern, path. A negated
	// pattern is a match that keeps the path in.
	fields := bytes.Split(output, []byte{0})
	var ignored []string
	for i := 0; i+3 < len(fields); i += 4 {
		source, pattern, file := string(fields[i]), fields[i+2], string(fields[i+3])
		if sources[source] && !bytes.HasPrefix(pattern, []byte("!")) {
			ignored = append(ignored, file)
		}
	}
	return ignored, nil
}

// isolatedEnv is the process environment without Git's own variables, which
// a hook sets to point at the caller's repository, and with no user or
// system configuration.
func isolatedEnv(empty string) []string {
	var env []string
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "GIT_") {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+empty)
}

// largest names the biggest top-level entries by key, each replaced by its
// biggest child when that child holds most of it.
func largest(files []treeFile, key string) []Entry {
	top := map[string]*Entry{}
	children := map[string]map[string]*Entry{}
	add := func(entries map[string]*Entry, name string, directory bool, size int64) {
		entry := entries[name]
		if entry == nil {
			entry = &Entry{Path: name, Directory: directory}
			entries[name] = entry
		}
		entry.Files++
		entry.Bytes += size
	}
	for _, file := range files {
		parts := strings.SplitN(file.path, "/", 3)
		add(top, parts[0], len(parts) > 1, file.bytes)
		if len(parts) > 1 {
			if children[parts[0]] == nil {
				children[parts[0]] = map[string]*Entry{}
			}
			add(children[parts[0]], parts[0]+"/"+parts[1], len(parts) > 2, file.bytes)
		}
	}
	value := func(entry *Entry) int64 {
		if key == RankedByFiles {
			return int64(entry.Files)
		}
		return entry.Bytes
	}
	var entries []*Entry
	for name, entry := range top {
		chosen := entry
		var biggest *Entry
		for _, child := range children[name] {
			if biggest == nil || value(child) > value(biggest) || (value(child) == value(biggest) && child.Path < biggest.Path) {
				biggest = child
			}
		}
		if biggest != nil && float64(value(biggest)) >= dominant*float64(value(entry)) {
			chosen = biggest
		}
		entries = append(entries, chosen)
	}
	sort.Slice(entries, func(i, j int) bool {
		if value(entries[i]) != value(entries[j]) {
			return value(entries[i]) > value(entries[j])
		}
		return entries[i].Path < entries[j].Path
	})
	if len(entries) > listed {
		entries = entries[:listed]
	}
	result := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		entry.Pattern = Pattern(entry.Path, entry.Directory)
		result = append(result, *entry)
	}
	return result
}

// Pattern is the gitignore line, matched from the repository root, that
// leaves out a directory and everything under it, or one file.
func Pattern(name string, directory bool) string {
	var escaped strings.Builder
	for i, r := range name {
		switch {
		case strings.ContainsRune(`\*?[`, r),
			i == 0 && (r == '#' || r == '!'):
			escaped.WriteByte('\\')
		}
		escaped.WriteRune(r)
	}
	pattern := escaped.String()
	if strings.HasSuffix(pattern, " ") {
		pattern = pattern[:len(pattern)-1] + `\ `
	}
	if directory {
		return pattern + "/**"
	}
	return "/" + pattern
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
