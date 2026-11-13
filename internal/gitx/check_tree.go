package gitx

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// MaxCheckDataBytes is the largest JSON-like or binary artifact sent with a
// check. Larger changed files stay in the captured tree but are omitted from
// the filtered patch tree.
const MaxCheckDataBytes int64 = 500000

// OmittedFile describes one changed path restored to its base-tree state.
type OmittedFile struct {
	Path   string
	Bytes  int64
	Reason string
}

// FilteredTree is a tree derived from a captured check tree, with selected
// large data files restored to their base-tree state.
type FilteredTree struct {
	Tree         string
	OmittedFiles []OmittedFile
}

type rawTreeChange struct {
	path       string
	oldMode    string
	oldObject  string
	newMode    string
	newObject  string
	oldBytes   int64
	newBytes   int64
	binaryFile bool
}

// FilterCheckTree makes a patch tree from an immutable captured tree. It
// classifies changed paths without creating a patch, then uses a private index
// to restore omitted paths to base. The caller's index and worktree are never
// changed, and tree itself remains the original captured snapshot.
func FilterCheckTree(root, base, tree string, include ...string) (FilteredTree, error) {
	includedPaths, err := resolveIncludedPaths(root, base, tree, include)
	if err != nil {
		return FilteredTree{}, err
	}
	numstat, err := RunBytes(root, nil,
		"-c", "core.quotePath=false",
		"diff", "--no-ext-diff", "--no-textconv", "--ignore-submodules=none",
		"--numstat", "-z", "--no-renames", base, tree,
	)
	if err != nil {
		return FilteredTree{}, err
	}
	binaryByPath, err := parseNumstat(numstat)
	if err != nil {
		return FilteredTree{}, err
	}
	if len(binaryByPath) == 0 {
		return FilteredTree{Tree: tree}, nil
	}

	raw, err := RunBytes(root, nil,
		"-c", "core.quotePath=false",
		"diff", "--no-ext-diff", "--no-textconv", "--ignore-submodules=none",
		"--raw", "--no-abbrev", "-z", "--no-renames", base, tree,
	)
	if err != nil {
		return FilteredTree{}, err
	}
	changes, objectIDs, err := parseRawChanges(raw, binaryByPath)
	if err != nil {
		return FilteredTree{}, err
	}
	sizes, err := objectSizes(root, objectIDs)
	if err != nil {
		return FilteredTree{}, err
	}
	for i := range changes {
		if changes[i].oldObject != "" {
			changes[i].oldBytes = sizes[changes[i].oldObject]
		}
		if changes[i].newObject != "" {
			changes[i].newBytes = sizes[changes[i].newObject]
		}
	}

	changeByPath := make(map[string]*rawTreeChange, len(changes))
	for i := range changes {
		changeByPath[changes[i].path] = &changes[i]
	}
	reasons := make(map[string]string)
	for i := range changes {
		change := &changes[i]
		if includedPaths[change.path] {
			continue
		}
		maxBytes := maxInt64(change.oldBytes, change.newBytes)
		if maxBytes <= MaxCheckDataBytes {
			continue
		}
		switch {
		case isJSONDataPath(change.path):
			reasons[change.path] = "large-json"
		case change.binaryFile:
			reasons[change.path] = "large-binary"
		}
	}

	// Git may recognize a rename even though the numstat classification above
	// deliberately disables renames. If one endpoint is omitted, omit the
	// other endpoint too so the filtered tree does not ship half a rename.
	renamePairs, err := detectedRenames(root, base, tree)
	if err != nil {
		return FilteredTree{}, err
	}
	for _, pair := range renamePairs {
		if includedPaths[pair[0]] || includedPaths[pair[1]] {
			delete(reasons, pair[0])
			delete(reasons, pair[1])
			continue
		}
		oldReason, newReason := reasons[pair[0]], reasons[pair[1]]
		if oldReason == "" && newReason == "" {
			continue
		}
		reason := oldReason
		if reason == "" || newReason == "large-json" {
			reason = newReason
		}
		if changeByPath[pair[0]] == nil || changeByPath[pair[1]] == nil {
			return FilteredTree{}, fmt.Errorf("git detected rename paths missing from no-renames diff: %q to %q", pair[0], pair[1])
		}
		reasons[pair[0]] = reason
		reasons[pair[1]] = reason
	}
	for _, group := range directoryFileChangeGroups(changes) {
		omittedPath := ""
		included := false
		for _, name := range group {
			if reasons[name] != "" && omittedPath == "" {
				omittedPath = name
			}
			if includedPaths[name] {
				included = true
			}
		}
		if omittedPath == "" {
			continue
		}
		if !included {
			return FilteredTree{}, fmt.Errorf(
				"cannot omit %q while preserving a file/directory transition involving %q; rerun with --include %q to retain the complete transition",
				omittedPath, group[0], group[0],
			)
		}
		for _, name := range group {
			delete(reasons, name)
		}
	}

	if len(reasons) == 0 {
		return FilteredTree{Tree: tree}, nil
	}

	omitted := make([]OmittedFile, 0, len(reasons))
	paths := make([]string, 0, len(reasons))
	for name := range reasons {
		change := changeByPath[name]
		if change == nil {
			return FilteredTree{}, fmt.Errorf("omitted path missing from tree diff: %q", name)
		}
		omitted = append(omitted, OmittedFile{
			Path:   name,
			Bytes:  maxInt64(change.oldBytes, change.newBytes),
			Reason: reasons[name],
		})
		paths = append(paths, name)
	}
	sort.Slice(omitted, func(i, j int) bool { return omitted[i].Path < omitted[j].Path })
	sort.Strings(paths)

	filteredTree, err := restorePathsToBase(root, tree, paths, changeByPath)
	if err != nil {
		return FilteredTree{}, err
	}
	return FilteredTree{Tree: filteredTree, OmittedFiles: omitted}, nil
}

// directoryFileChangeGroups identifies no-renames diff paths that replace a
// file with a directory or a directory with a file. Such paths must be kept
// together: restoring only the omitted endpoint either conflicts in the
// index or drops the other side of the transition.
func directoryFileChangeGroups(changes []rawTreeChange) [][]string {
	byPath := make(map[string]*rawTreeChange, len(changes))
	parents := make(map[string]string)
	for i := range changes {
		byPath[changes[i].path] = &changes[i]
		parents[changes[i].path] = changes[i].path
	}
	var find func(string) string
	find = func(name string) string {
		parent := parents[name]
		if parent != name {
			parents[name] = find(parent)
		}
		return parents[name]
	}
	union := func(a, b string) {
		ra, rb := find(a), find(b)
		if ra != rb {
			if ra < rb {
				parents[rb] = ra
			} else {
				parents[ra] = rb
			}
		}
	}
	for _, child := range changes {
		for separator := strings.IndexByte(child.path, '/'); separator >= 0; {
			ancestorPath := child.path[:separator]
			ancestor := byPath[ancestorPath]
			if ancestor != nil {
				fileToDirectory := ancestor.oldMode != "000000" && ancestor.newMode == "000000" &&
					child.oldMode == "000000" && child.newMode != "000000"
				directoryToFile := ancestor.oldMode == "000000" && ancestor.newMode != "000000" &&
					child.oldMode != "000000" && child.newMode == "000000"
				if fileToDirectory || directoryToFile {
					union(ancestorPath, child.path)
				}
			}
			next := strings.IndexByte(child.path[separator+1:], '/')
			if next < 0 {
				break
			}
			separator += next + 1
		}
	}
	grouped := make(map[string][]string)
	for name := range parents {
		root := find(name)
		grouped[root] = append(grouped[root], name)
	}
	var groups [][]string
	for _, group := range grouped {
		if len(group) < 2 {
			continue
		}
		sort.Strings(group)
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i][0] < groups[j][0] })
	return groups
}

// resolveIncludedPaths selects only paths present in the captured base-to-tree
// diff. Each pattern is tried as an exact literal first, so metacharacters in
// a real filename cannot accidentally broaden an exact-path inclusion; when
// no literal path matches, Git's glob pathspec semantics are used.
func resolveIncludedPaths(root, base, tree string, patterns []string) (map[string]bool, error) {
	included := make(map[string]bool)
	for _, pattern := range patterns {
		if pattern == "" {
			return nil, fmt.Errorf("include path must not be empty")
		}
		literal, err := diffPathspecMatches(root, base, tree, "literal", pattern)
		if err != nil {
			return nil, err
		}
		matches := literal
		if len(literal) == 0 {
			matches, err = diffPathspecMatches(root, base, tree, "glob", pattern)
			if err != nil {
				return nil, err
			}
		}
		for _, name := range matches {
			included[name] = true
		}
	}
	return included, nil
}

func diffPathspecMatches(root, base, tree, kind, pattern string) ([]string, error) {
	pathspec := ":(top," + kind + ")" + pattern
	output, err := RunBytes(root, nil,
		"-c", "core.quotePath=false",
		"diff", "--no-ext-diff", "--no-textconv", "--ignore-submodules=none",
		"--name-only", "-z", "--no-renames", base, tree, "--", pathspec,
	)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, name := range bytes.Split(output, []byte{0}) {
		if len(name) != 0 {
			paths = append(paths, string(name))
		}
	}
	return paths, nil
}

// parseNumstat returns the Git binary classification for each changed path.
// With -z and --no-renames, each NUL-delimited record is
// "added<TAB>deleted<TAB>path"; only the first two tabs are structural, so
// tabs, newlines, and other unusual bytes in the path remain intact.
func parseNumstat(output []byte) (map[string]bool, error) {
	binaryByPath := make(map[string]bool)
	for _, record := range bytes.Split(output, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		added, rest, ok := bytes.Cut(record, []byte{'\t'})
		if !ok {
			return nil, fmt.Errorf("malformed git numstat record: missing added count")
		}
		deleted, name, ok := bytes.Cut(rest, []byte{'\t'})
		if !ok || len(name) == 0 {
			return nil, fmt.Errorf("malformed git numstat record: missing path")
		}
		binary := bytes.Equal(added, []byte("-")) && bytes.Equal(deleted, []byte("-"))
		if !binary {
			if bytes.Equal(added, []byte("-")) || bytes.Equal(deleted, []byte("-")) {
				return nil, fmt.Errorf("malformed git numstat record for %q: only one count is binary", name)
			}
			if _, err := strconv.ParseInt(string(added), 10, 64); err != nil {
				return nil, fmt.Errorf("malformed git numstat added count for %q: %w", name, err)
			}
			if _, err := strconv.ParseInt(string(deleted), 10, 64); err != nil {
				return nil, fmt.Errorf("malformed git numstat deleted count for %q: %w", name, err)
			}
		}
		path := string(name)
		if _, exists := binaryByPath[path]; exists {
			return nil, fmt.Errorf("duplicate path in git numstat output: %q", path)
		}
		binaryByPath[path] = binary
	}
	return binaryByPath, nil
}

func parseRawChanges(output []byte, binaryByPath map[string]bool) ([]rawTreeChange, []string, error) {
	tokens := bytes.Split(output, []byte{0})
	changes := make([]rawTreeChange, 0, len(binaryByPath))
	objects := make([]string, 0, len(binaryByPath)*2)
	seen := make(map[string]bool, len(binaryByPath))
	for i := 0; i+1 < len(tokens); {
		metadata := tokens[i]
		name := tokens[i+1]
		i += 2
		fields := bytes.Fields(metadata)
		if len(fields) != 5 || len(fields[0]) < 2 || fields[0][0] != ':' || len(name) == 0 {
			return nil, nil, fmt.Errorf("malformed git raw diff record")
		}
		oldMode := string(fields[0][1:])
		newMode := string(fields[1])
		oldObject := string(fields[2])
		newObject := string(fields[3])
		nameString := string(name)
		binary, exists := binaryByPath[nameString]
		if !exists {
			return nil, nil, fmt.Errorf("git raw diff path missing from numstat output: %q", nameString)
		}
		if seen[nameString] {
			return nil, nil, fmt.Errorf("duplicate path in git raw diff output: %q", nameString)
		}
		seen[nameString] = true
		change := rawTreeChange{
			path:       nameString,
			oldMode:    oldMode,
			oldObject:  objectIfPresent(oldMode, oldObject),
			newMode:    newMode,
			newObject:  objectIfPresent(newMode, newObject),
			binaryFile: binary,
		}
		if change.oldObject != "" && isBlobMode(oldMode) {
			objects = append(objects, change.oldObject)
		}
		if change.newObject != "" && isBlobMode(newMode) {
			objects = append(objects, change.newObject)
		}
		changes = append(changes, change)
	}
	if len(seen) != len(binaryByPath) {
		for name := range binaryByPath {
			if !seen[name] {
				return nil, nil, fmt.Errorf("git numstat path missing from raw diff output: %q", name)
			}
		}
	}
	return changes, uniqueStrings(objects), nil
}

func objectIfPresent(mode, object string) string {
	if mode == "000000" || strings.Trim(object, "0") == "" {
		return ""
	}
	return object
}

func isBlobMode(mode string) bool {
	return strings.HasPrefix(mode, "100") || mode == "120000"
}

func objectSizes(root string, objects []string) (map[string]int64, error) {
	sizes := make(map[string]int64, len(objects))
	for _, batch := range chunks(objects, 512) {
		var input bytes.Buffer
		for _, object := range batch {
			input.WriteString(object)
			input.WriteByte('\n')
		}
		output, err := RunInput(root, nil, input.Bytes(), "cat-file", "--batch-check")
		if err != nil {
			return nil, err
		}
		lines := bytes.Split(bytes.TrimSuffix(output, []byte{'\n'}), []byte{'\n'})
		if len(lines) != len(batch) {
			return nil, fmt.Errorf("git cat-file returned %d sizes for %d objects", len(lines), len(batch))
		}
		for i, line := range lines {
			fields := bytes.Fields(line)
			if len(fields) != 3 || string(fields[1]) != "blob" {
				return nil, fmt.Errorf("git cat-file could not read blob %s: %s", batch[i], line)
			}
			size, err := strconv.ParseInt(string(fields[2]), 10, 64)
			if err != nil {
				return nil, fmt.Errorf("parse git blob size for %s: %w", batch[i], err)
			}
			sizes[batch[i]] = size
		}
	}
	return sizes, nil
}

func detectedRenames(root, base, tree string) ([][2]string, error) {
	output, err := RunBytes(root, nil,
		"-c", "core.quotePath=false",
		"diff", "--no-ext-diff", "--no-textconv", "--ignore-submodules=none",
		"--name-status", "-z", "--find-renames", base, tree,
	)
	if err != nil {
		return nil, err
	}
	tokens := bytes.Split(output, []byte{0})
	var pairs [][2]string
	for i := 0; i < len(tokens); {
		status := tokens[i]
		i++
		if len(status) == 0 {
			continue
		}
		pathCount := 1
		if status[0] == 'R' || status[0] == 'C' {
			pathCount = 2
		}
		if i+pathCount > len(tokens)-1 {
			return nil, fmt.Errorf("malformed git name-status rename output")
		}
		first := string(tokens[i])
		i++
		if pathCount == 2 {
			second := string(tokens[i])
			i++
			if status[0] == 'R' {
				pairs = append(pairs, [2]string{first, second})
			}
		}
	}
	return pairs, nil
}

func restorePathsToBase(root, tree string, paths []string, changes map[string]*rawTreeChange) (string, error) {
	dir, err := os.MkdirTemp("", "slopcop-filter-index-")
	if err != nil {
		return "", fmt.Errorf("create temporary index: %w", err)
	}
	defer os.RemoveAll(dir)
	env := withEnv(os.Environ(), "GIT_INDEX_FILE", filepath.Join(dir, "index"))
	if _, err := RunBytes(root, env, "read-tree", tree); err != nil {
		return "", err
	}
	var entries bytes.Buffer
	var removals bytes.Buffer
	for _, name := range paths {
		change := changes[name]
		if change == nil {
			return "", fmt.Errorf("missing raw diff metadata for omitted path %q", name)
		}
		if change.oldMode == "000000" {
			removals.WriteString(name)
			removals.WriteByte(0)
			continue
		}
		fmt.Fprintf(&entries, "%s %s 0\t", change.oldMode, change.oldObject)
		entries.WriteString(name)
		entries.WriteByte(0)
	}
	if entries.Len() > 0 {
		if _, err := RunInput(root, env, entries.Bytes(), "update-index", "-z", "--index-info"); err != nil {
			return "", err
		}
	}
	if removals.Len() > 0 {
		if _, err := RunInput(root, env, removals.Bytes(), "update-index", "-z", "--force-remove", "--stdin"); err != nil {
			return "", err
		}
	}
	return writeTree(root, env)
}

func isJSONDataPath(name string) bool {
	extension := strings.ToLower(path.Ext(name))
	return extension == ".json" || extension == ".jsonl" || extension == ".ndjson"
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			unique = append(unique, value)
		}
	}
	return unique
}
