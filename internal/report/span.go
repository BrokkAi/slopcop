package report

import (
	"bytes"
	"fmt"
	"unicode/utf8"

	"github.com/BrokkAi/slopcop/internal/service"
)

// maxQuotedSpan is the longest span, in bytes, a human report quotes. A
// longer one is a statement or a block, which its location already finds.
const maxQuotedSpan = 80

// Source reads a checked file by its repository path, as the check uploaded
// it. ok is false when the file cannot be read.
type Source func(path string) (content []byte, ok bool)

// findingLocation is a finding's path, line and start column, such as
// `src/app.ts:16:11`, as editors and terminals open it.
func findingLocation(finding service.Finding) string {
	location := printable(finding.Path)
	if finding.StartLine == nil {
		return location
	}
	location = fmt.Sprintf("%s:%d", location, *finding.StartLine)
	if column := finding.StartColumn; column != nil && *column >= 1 {
		location = fmt.Sprintf("%s:%d", location, *column)
	}
	return location
}

// spanQuoter returns the text of a finding's span in source, or "" when it
// cannot quote it exactly. It reads each file once.
func spanQuoter(source Source) func(service.Finding) string {
	if source == nil {
		return func(service.Finding) string { return "" }
	}
	files := map[string][]byte{}
	return func(finding service.Finding) string {
		if !quotable(finding) {
			return ""
		}
		content, read := files[finding.Path]
		if !read {
			content, _ = source(finding.Path)
			files[finding.Path] = content
		}
		return spanText(content, *finding.StartLine, *finding.StartColumn, *finding.EndColumn)
	}
}

// quotable says a finding has a short span on one line.
func quotable(finding service.Finding) bool {
	if finding.StartLine == nil || finding.EndLine == nil || finding.StartColumn == nil || finding.EndColumn == nil {
		return false
	}
	start, end := *finding.StartColumn, *finding.EndColumn
	return *finding.StartLine >= 1 && *finding.EndLine == *finding.StartLine &&
		start >= 1 && end > start && end-start <= maxQuotedSpan
}

// spanText is the text from 1-based column start up to end on 1-based line in
// content. Analyzers count columns in bytes, characters or UTF-16 units,
// which agree only on ASCII text, so a line with anything else before end
// quotes nothing rather than the wrong text.
func spanText(content []byte, line, start, end int) string {
	for current := 1; current < line; current++ {
		next := bytes.IndexByte(content, '\n')
		if next < 0 {
			return ""
		}
		content = content[next+1:]
	}
	if next := bytes.IndexByte(content, '\n'); next >= 0 {
		content = content[:next]
	}
	if end-1 > len(content) {
		return ""
	}
	prefix := content[:end-1]
	for _, b := range prefix {
		if b >= utf8.RuneSelf {
			return ""
		}
	}
	span := prefix[start-1:]
	if bytes.ContainsAny(span, "\r\t") {
		return ""
	}
	return string(span)
}
