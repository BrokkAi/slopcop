package prcomment

import (
	"strconv"
	"strings"
)

// maxAnchorSpan bounds how many lines of a finding's range are tried for a
// line the diff shows, so a finding spanning a whole file costs no more than
// a short one.
const maxAnchorSpan = 500

// diffLines is the new-side lines of one file that the pull request's diff
// shows, each mapped to the hunk that holds it. GitHub takes an inline
// comment only on such a line, and a multi-line comment only on a range in
// one hunk.
type diffLines map[int]int

// parsePatch reads the new-side line numbers out of a file's unified diff, as
// the pull request files API sends it: added and context lines count, removed
// lines do not. A file GitHub sent no patch for, such as a binary or a very
// large one, has none.
func parsePatch(patch string) diffLines {
	lines := diffLines{}
	hunk, next := -1, 0
	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "@@") {
			start, ok := hunkStart(line)
			if !ok {
				hunk = -1
				continue
			}
			hunk++
			next = start
			continue
		}
		if hunk < 0 {
			continue
		}
		switch {
		case strings.HasPrefix(line, "+"), strings.HasPrefix(line, " "):
			lines[next] = hunk
			next++
		}
		// A removed line, "\ No newline at end of file", and the empty
		// string after a trailing newline hold no new-side line.
	}
	return lines
}

// hunkStart reads the new-side start line of a hunk header such as
// "@@ -10,7 +12,9 @@ func f() {".
func hunkStart(header string) (int, bool) {
	plus := strings.Index(header, " +")
	if plus < 0 {
		return 0, false
	}
	rest := header[plus+2:]
	end := strings.IndexAny(rest, ", ")
	if end < 0 {
		return 0, false
	}
	start, err := strconv.Atoi(rest[:end])
	if err != nil || start < 0 {
		return 0, false
	}
	return start, true
}

// anchor places a finding at start..end (end 0 for a single line) on lines
// the diff shows. It keeps the whole range when both ends are in one hunk,
// and otherwise falls back to the first line of the range the diff shows.
// ok is false when no line of the range is in the diff: the finding then
// belongs in the summary comment, since GitHub refuses a comment there.
func (d diffLines) anchor(start, end int) (from, to int, ok bool) {
	if start <= 0 {
		return 0, 0, false
	}
	if end < start {
		end = start
	}
	startHunk, startShown := d[start]
	endHunk, endShown := d[end]
	if startShown && endShown && startHunk == endHunk {
		return start, end, true
	}
	for line := start; line <= end && line < start+maxAnchorSpan; line++ {
		if _, shown := d[line]; shown {
			return line, line, true
		}
	}
	return 0, 0, false
}
