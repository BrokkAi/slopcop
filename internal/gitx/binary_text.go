package gitx

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// maxBinaryFileText and maxBinaryText bound what BinaryFileText returns
	// for one file and for the whole change.
	maxBinaryFileText = 16 * 1024
	maxBinaryText     = 64 * 1024
	// minBinaryRun is the shortest readable run kept from a line that is
	// mostly binary. Shorter runs are common by chance in compressed data.
	minBinaryRun = 8
	// maxListedBinaries is how many changed binary files a prompt names.
	maxListedBinaries = 200
)

// BinaryFile is a changed file git calls binary.
type BinaryFile struct {
	Path  string
	Bytes int64
	// Format is the image format every version the change touches decodes
	// as, with nothing after the image, or "" when they do not.
	Format string
	// Deleted says the change removes the file; Bytes and Format then
	// describe the version it removes.
	Deleted bool
}

// BinaryText is what a reviewer is told about a change to files git calls
// binary.
type BinaryText struct {
	// Text names the changed binary files and their changed readable lines.
	// It is "" when the change has no binary file.
	Text string
	// Complete is false when a size limit left a changed line or file out.
	Complete bool
}

// readableHunk matches the header of a -U0 hunk of two rendered files.
var readableHunk = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// BinaryFileText describes the readable lines a change adds, removes or moves
// in files git calls binary, which a diff shows only as "Binary files differ".
// One NUL byte is enough for that label, and a shell runs straight past it, so
// a script could otherwise reach a reviewer as no text at all.
//
// NUL bytes are dropped, which also makes UTF-16 text readable. A mostly
// readable line is kept with its other bytes escaped; from any other line only
// long readable runs are kept, so real binary data stays out. Every content
// line starts with "+<line>: " or "-<line>: ", so a file cannot forge a header.
func BinaryFileText(root, base, tree string) (BinaryText, error) {
	result := BinaryText{Complete: true}
	binary, sizes, err := changedBinaries(root, base, tree)
	if err != nil || len(binary) == 0 {
		return result, err
	}
	files, err := inspectBinaries(root, binary, sizes)
	if err != nil {
		return result, err
	}

	var body strings.Builder
	unscanned := 0
	for _, change := range binary {
		remaining := maxBinaryText - body.Len()
		if remaining <= 0 {
			unscanned++
			continue
		}
		if size := maxInt64(sizes[change.oldObject], sizes[change.newObject]); size > MaxCheckDataBytes {
			fmt.Fprintf(&body, "File %q: %d bytes, not scanned for readable text.\n", change.path, size)
			result.Complete = false
			continue
		}
		section, complete, err := binaryFileSection(root, change, min(remaining, maxBinaryFileText))
		if err != nil {
			return result, err
		}
		body.WriteString(section)
		result.Complete = result.Complete && complete
	}
	if unscanned > 0 {
		fmt.Fprintf(&body, "[%d more binary files were not scanned for readable text. Read them with file-reading tools.]\n", unscanned)
		result.Complete = false
	}
	result.Text = binaryFileList(files)
	if body.Len() > 0 {
		result.Text += "Readable text changed in files git treats as binary. Other bytes are escaped as \\xNN or left out, and NUL bytes are dropped. On a mostly binary line only runs of 8 or more readable characters are shown, so a shorter command there would not appear. Line numbers are the file's.\n" +
			body.String() + "\n"
	}
	return result, nil
}

// InspectBinaryFiles lists the files git calls binary that tree adds, changes
// or removes compared with base, each with the image format it decodes as, if
// any.
func InspectBinaryFiles(root, base, tree string) ([]BinaryFile, error) {
	binary, sizes, err := changedBinaries(root, base, tree)
	if err != nil || len(binary) == 0 {
		return nil, err
	}
	return inspectBinaries(root, binary, sizes)
}

// changedBinaries is the changes to regular files git calls binary, with the
// sizes of their blobs.
func changedBinaries(root, base, tree string) ([]rawTreeChange, map[string]int64, error) {
	numstat, err := RunWithoutRepoAttributes(root,
		"-c", "core.quotePath=false",
		"diff", "--no-ext-diff", "--no-textconv", "--ignore-submodules=none",
		"--numstat", "-z", "--no-renames", base, tree,
	)
	if err != nil {
		return nil, nil, err
	}
	binaryByPath, err := parseNumstat(numstat)
	if err != nil {
		return nil, nil, err
	}
	raw, err := RunBytes(root, nil,
		"-c", "core.quotePath=false",
		"diff", "--no-ext-diff", "--no-textconv", "--ignore-submodules=none",
		"--raw", "--no-abbrev", "-z", "--no-renames", base, tree,
	)
	if err != nil {
		return nil, nil, err
	}
	changes, _, err := parseRawChanges(raw, binaryByPath)
	if err != nil {
		return nil, nil, err
	}
	var binary []rawTreeChange
	var objects []string
	for _, change := range changes {
		if !change.binaryFile || (!isRegularMode(change.oldMode) && !isRegularMode(change.newMode)) {
			continue
		}
		binary = append(binary, change)
		if isRegularMode(change.oldMode) {
			objects = append(objects, change.oldObject)
		}
		if isRegularMode(change.newMode) {
			objects = append(objects, change.newObject)
		}
	}
	if len(binary) == 0 {
		return nil, nil, nil
	}
	sizes, err := objectSizes(root, uniqueStrings(objects))
	return binary, sizes, err
}

// inspectBinaries parses every version of each file the change touches. A file
// that replaces an unverifiable version stays unverified, since the bytes it
// removes were not reviewed either.
func inspectBinaries(root string, binary []rawTreeChange, sizes map[string]int64) ([]BinaryFile, error) {
	var files []BinaryFile
	for _, change := range binary {
		object, deleted := change.newObject, false
		if !isRegularMode(change.newMode) {
			object, deleted = change.oldObject, true
		}
		format, err := blobFormat(root, object, sizes)
		if err != nil {
			return nil, err
		}
		if format != "" && !deleted && isRegularMode(change.oldMode) {
			old, err := blobFormat(root, change.oldObject, sizes)
			if err != nil {
				return nil, err
			}
			if old == "" {
				format = ""
			}
		}
		files = append(files, BinaryFile{Path: change.path, Bytes: sizes[object], Format: format, Deleted: deleted})
	}
	return files, nil
}

// blobFormat parses one blob. One over the size filter, which only --include
// keeps, is not read and stays unverified.
func blobFormat(root, object string, sizes map[string]int64) (string, error) {
	if sizes[object] > MaxCheckDataBytes {
		return "", nil
	}
	blob, err := RunBytes(root, nil, "cat-file", "blob", object)
	if err != nil {
		return "", err
	}
	return parseBinaryFormat(blob), nil
}

// binaryFileList names the changed binary files for a reviewer, unverified
// ones first so that the cap on the list cannot hide them.
func binaryFileList(files []BinaryFile) string {
	if len(files) == 0 {
		return ""
	}
	var unverified, verified []BinaryFile
	for _, file := range files {
		if file.Format == "" {
			unverified = append(unverified, file)
		} else {
			verified = append(verified, file)
		}
	}
	var out strings.Builder
	out.WriteString("Changed binary files. Their bytes are not in this prompt.\n")
	for i, file := range append(unverified, verified...) {
		if i == maxListedBinaries {
			fmt.Fprintf(&out, "[%d more not listed.]\n", len(files)-i)
			break
		}
		state := ""
		if file.Deleted {
			state = "deleted, "
		}
		if file.Format == "" {
			fmt.Fprintf(&out, "- %q: %sno recognized format covers every version of the file this change touches. Treat its content as unreviewed, apart from any text shown below.\n", file.Path, state)
		} else {
			fmt.Fprintf(&out, "- %q: %sdecodes as %s, with nothing after the image. Data could still be hidden inside it.\n", file.Path, state, file.Format)
		}
	}
	out.WriteString("\n")
	return out.String()
}

// binaryFileSection lists the readable lines that differ between the two
// sides of change in at most limit bytes, and says whether all of them fit.
func binaryFileSection(root string, change rawTreeChange, limit int) (string, bool, error) {
	before, err := readableBlob(root, change.oldMode, change.oldObject)
	if err != nil {
		return "", false, err
	}
	after, err := readableBlob(root, change.newMode, change.newObject)
	if err != nil {
		return "", false, err
	}
	diff, err := diffReadable(before, after)
	if err != nil {
		return "", false, err
	}
	header := fmt.Sprintf("File %q:\n", change.path)
	var shown strings.Builder
	omitted, oldLine, newLine := 0, 0, 0
	inHunk := false
	for _, line := range strings.Split(diff, "\n") {
		if match := readableHunk.FindStringSubmatch(line); match != nil {
			oldLine, _ = strconv.Atoi(match[1])
			newLine, _ = strconv.Atoi(match[2])
			inHunk = true
			continue
		}
		if !inHunk || line == "" {
			continue
		}
		var number int
		switch line[0] {
		case '-':
			number = oldLine
			oldLine++
		case '+':
			number = newLine
			newLine++
		default: // "\ No newline at end of file"
			continue
		}
		if line[1:] == "" {
			continue
		}
		entry := fmt.Sprintf("%c%d: %s\n", line[0], number, line[1:])
		if len(header)+shown.Len()+len(entry) > limit {
			omitted++
			continue
		}
		shown.WriteString(entry)
	}
	if shown.Len() == 0 && omitted == 0 {
		return "", true, nil
	}
	if omitted > 0 {
		fmt.Fprintf(&shown, "[%d more changed lines not shown. Read the file with file-reading tools.]\n", omitted)
	}
	return header + shown.String(), omitted == 0, nil
}

// readableBlob renders one side of a change, "" for a side with no file.
func readableBlob(root, mode, object string) (string, error) {
	if !isRegularMode(mode) {
		return "", nil
	}
	blob, err := RunBytes(root, nil, "cat-file", "blob", object)
	if err != nil {
		return "", err
	}
	return renderReadable(blob), nil
}

// diffReadable compares two rendered files. They hold only printable text, so
// --text is safe, and it overrides any attributes around the temporary files.
func diffReadable(before, after string) (string, error) {
	dir, err := os.MkdirTemp("", "slopcop-binary-text-")
	if err != nil {
		return "", fmt.Errorf("create readable text directory: %w", err)
	}
	defer os.RemoveAll(dir)
	for name, content := range map[string]string{"old": before, "new": after} {
		if err = os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			return "", fmt.Errorf("write readable text: %w", err)
		}
	}
	command := exec.Command("git", "diff", "--no-index", "--no-color", "--no-ext-diff", "--no-textconv", "--text", "-U0", "--inter-hunk-context=0", "--", "old", "new")
	command.Dir = dir
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err = command.Run()
	// With --no-index, exit status 1 means the files differ.
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
		return "", fmt.Errorf("compare readable text: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// renderReadable keeps the file's line numbering: a line with nothing readable
// becomes an empty line.
func renderReadable(blob []byte) string {
	lines := bytes.Split(bytes.ReplaceAll(blob, []byte{0}, nil), []byte{'\n'})
	rendered := make([]string, len(lines))
	for i, line := range lines {
		rendered[i] = readableText(bytes.TrimRight(line, "\r"))
	}
	return strings.Join(rendered, "\n")
}

// readableText renders a line whose characters are at least three quarters
// printable with the rest escaped, and any other line as its long printable
// runs. Format characters such as bidi controls count as not printable.
func readableText(line []byte) string {
	var whole, run strings.Builder
	var runs []string
	endRun := func() {
		if len(strings.TrimSpace(run.String())) >= minBinaryRun {
			runs = append(runs, run.String())
		}
		run.Reset()
	}
	printable, total := 0, 0
	for rest := line; len(rest) > 0; total++ {
		r, size := utf8.DecodeRune(rest)
		if (r != utf8.RuneError || size > 1) && (unicode.IsPrint(r) || r == '\t') {
			whole.Write(rest[:size])
			run.Write(rest[:size])
			printable++
		} else {
			for _, b := range rest[:size] {
				fmt.Fprintf(&whole, `\x%02x`, b)
			}
			endRun()
		}
		rest = rest[size:]
	}
	endRun()
	if strings.TrimSpace(whole.String()) == "" {
		return ""
	}
	if printable*4 >= total*3 {
		return whole.String()
	}
	return strings.Join(runs, " … ")
}

func isRegularMode(mode string) bool { return strings.HasPrefix(mode, "100") }
