package review

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Finding is one finding a reviewer reported, parsed from its reply. The
// rubric asks for `- [Pn] title path:start-end` followed by a paragraph;
// reviewers mostly follow it but vary the punctuation, so the parse is
// lenient and keeps whatever it cannot place in Message.
type Finding struct {
	// Reviewer is the Name of the reviewer that reported the finding.
	Reviewer string `json:"reviewer"`
	// Priority is the reviewer's tag, "P0" (most urgent) to "P3". It is
	// empty when the reviewer did not tag the finding.
	Priority string `json:"priority,omitempty"`
	// Path is the repository-relative file the finding names, if any.
	Path string `json:"path,omitempty"`
	// StartLine and EndLine are the line range after Path; EndLine equals
	// StartLine for a single line. Both are nil without a location.
	StartLine *int   `json:"startLine,omitempty"`
	EndLine   *int   `json:"endLine,omitempty"`
	Title     string `json:"title"`
	// Message is the reviewer's explanation, joined into one paragraph.
	Message string `json:"message,omitempty"`
}

var (
	// findingStart matches a list item whose text begins with a priority
	// tag, optionally in bold: "- [P1] ...", "1. **[P2] ...".
	findingStart = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+(?:\*\*|__)?\[(P[0-3])\]\s*(.*)$`)
	// quotedLocation matches a path:line or path:start-end in backticks,
	// where the path may hold any character, spaces and brackets included.
	quotedLocation = regexp.MustCompile("`([^`\\n]+?):(\\d+)(?:\\s*[-–]\\s*(\\d+))?`")
	// bareLocation matches one outside backticks. Brackets and parentheses
	// are allowed after the first slash, as in `app/[id]/page.tsx` or
	// `app/(admin)/page.tsx`, so a markdown link such as
	// `[AGENTS.md:15](...)` still yields `AGENTS.md`. The rubric asks for
	// backticks, which quotedLocation reads exactly; this is the fallback.
	bareLocation = regexp.MustCompile(`(?:^|[\s(\[])([\p{L}\p{N}_@+.\-]+(?:/[\p{L}\p{N}_@+.\-\[\]()]+)*):(\d+)(?:\s*[-–]\s*(\d+))?`)
)

// ParseFindings splits one reviewer's reply into findings. A reply that has
// text but no tagged list item is one untagged finding holding the whole
// reply, so a reviewer that reported findings never counts as zero. changed
// holds the paths the review covered; an unquoted location that ends a path
// with spaces in it, such as `File.go:12` of `src/My File.go:12`, is widened
// to the changed path it ends. It may be nil.
func ParseFindings(reviewer, text string, changed map[string]bool) []Finding {
	var findings []Finding
	var body []string
	flush := func() {
		if len(findings) == 0 {
			return
		}
		last := &findings[len(findings)-1]
		last.Message = joinParagraph(append([]string{last.Message}, body...))
		body = nil
	}
	for _, line := range strings.Split(text, "\n") {
		match := findingStart.FindStringSubmatch(line)
		if match == nil {
			if len(findings) > 0 {
				body = append(body, line)
			}
			continue
		}
		flush()
		findings = append(findings, headerFinding(reviewer, match[1], match[2], changed))
	}
	flush()
	if len(findings) == 0 {
		if title, rest := firstLine(text); title != "" {
			findings = append(findings, Finding{Reviewer: reviewer, Title: title, Message: joinParagraph(rest)})
		}
	}
	return findings
}

// headerFinding parses the text after a priority tag: the title, then the
// location, then possibly the start of the explanation.
func headerFinding(reviewer, priority, header string, changed map[string]bool) Finding {
	finding := Finding{Reviewer: reviewer, Priority: priority}
	loc := firstLocation(header)
	if loc == nil {
		finding.Title = trimDecoration(header)
		return finding
	}
	widenToChangedPath(header, loc, changed)
	finding.Path = strings.TrimPrefix(header[loc[2]:loc[3]], "./")
	start, _ := strconv.Atoi(header[loc[4]:loc[5]])
	end := start
	if loc[6] >= 0 {
		if n, err := strconv.Atoi(header[loc[6]:loc[7]]); err == nil && n >= start {
			end = n
		}
	}
	finding.StartLine, finding.EndLine = &start, &end
	finding.Title = trimDecoration(header[:loc[0]])
	finding.Message = strings.TrimLeft(header[loc[1]:], " \t`*_)]:;,.—–-")
	return finding
}

// widenToChangedPath moves the start of a location's path left over the
// words before it to the longest changed path it ends, so a path with spaces
// is not cut to its last word even when that word is itself a changed path.
// loc's path and match start move together.
func widenToChangedPath(header string, loc []int, changed map[string]bool) {
	if len(changed) == 0 {
		return
	}
	for start := 0; start < loc[2]; start++ {
		if start > 0 && header[start-1] != ' ' && header[start-1] != '\t' {
			continue
		}
		path := strings.TrimLeft(header[start:loc[3]], " \t`*_([—–-")
		if changed[strings.TrimPrefix(path, "./")] {
			loc[2] = loc[3] - len(path)
			loc[0] = loc[2]
			return
		}
	}
}

// ChangedPaths reads the paths of a `git diff --numstat` listing, taking
// the new path of a rename.
func ChangedPaths(numstat string) map[string]bool {
	paths := map[string]bool{}
	for _, line := range strings.Split(numstat, "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 || fields[2] == "" {
			continue
		}
		path := fields[2]
		if unquoted, err := strconv.Unquote(path); err == nil && strings.HasPrefix(path, `"`) {
			path = unquoted
		}
		paths[renamedTo(path)] = true
	}
	return paths
}

// renamedTo is the new path of a numstat rename, `old => new` or
// `dir/{old => new}/file`, and path itself otherwise.
func renamedTo(path string) string {
	openBrace, closeBrace := strings.Index(path, "{"), strings.LastIndex(path, "}")
	if openBrace >= 0 && closeBrace > openBrace {
		if _, after, ok := strings.Cut(path[openBrace+1:closeBrace], " => "); ok {
			joined := path[:openBrace] + after + path[closeBrace+1:]
			return strings.TrimPrefix(strings.ReplaceAll(joined, "//", "/"), "/")
		}
	}
	if _, after, ok := strings.Cut(path, " => "); ok {
		return after
	}
	return path
}

// firstLocation returns the submatch indexes of the earliest location in
// header, quoted or bare. Only the earliest counts: a later one is usually a
// rule citation in the explanation, not the place the finding is about. A
// bare candidate with no letter in its path, such as a time like 10:30, is
// passed over.
func firstLocation(header string) []int {
	var best []int
	consider := func(candidates [][]int) {
		for _, candidate := range candidates {
			if !strings.ContainsFunc(header[candidate[2]:candidate[3]], unicode.IsLetter) {
				continue
			}
			if best == nil || candidate[0] < best[0] {
				best = candidate
			}
			return
		}
	}
	consider(quotedLocation.FindAllStringSubmatchIndex(header, -1))
	consider(bareLocation.FindAllStringSubmatchIndex(header, -1))
	return best
}

// trimDecoration strips the separators and markdown reviewers put around a
// title or location: dashes, colons, brackets, backticks and bold markers.
func trimDecoration(s string) string {
	s = strings.TrimSpace(s)
	for {
		trimmed := strings.Trim(s, " \t`*_()[]:;,.—–-")
		trimmed = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(trimmed), " in"))
		trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, " at"))
		if trimmed == s {
			return s
		}
		s = trimmed
	}
}

// joinParagraph joins non-blank lines with single spaces.
func joinParagraph(lines []string) string {
	var parts []string
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			parts = append(parts, line)
		}
	}
	return strings.Join(parts, " ")
}

// firstLine returns the first non-blank line of text and the lines after it.
func firstLine(text string) (string, []string) {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			return line, lines[i+1:]
		}
	}
	return "", nil
}

// Findings parses the replies of every reviewer that reported findings, in
// reviewer order.
func (v Verdict) Findings() []Finding {
	findings := []Finding{}
	changed := ChangedPaths(v.Capture.Numstat)
	for _, r := range v.Reviewers {
		if r.Status == StatusFindings {
			findings = append(findings, ParseFindings(r.Name, r.Text, changed)...)
		}
	}
	return findings
}
