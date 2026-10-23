// Package terminal keeps hostile text, from an analyzed repository or a
// response body, from driving the terminal it is printed to.
package terminal

import (
	"strings"
	"unicode"
)

// Safe replaces control characters (ANSI escapes among them) and the
// bidirectional embedding, override and isolate characters with U+FFFD, and
// invalid UTF-8 with the same, so value prints as the text it is.
func Safe(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || (r >= '‪' && r <= '‮') || (r >= '⁦' && r <= '⁩') {
			return unicode.ReplacementChar
		}
		return r
	}, value)
}
