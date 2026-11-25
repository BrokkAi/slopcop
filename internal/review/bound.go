package review

import "unicode/utf8"

const changedFilesLimit = 32 << 10
const userMessagesLimit = 128 << 10

func boundReviewSection(text string, limit int, label string) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	marker := "\n…[" + label + " omitted]…\n"
	if limit <= len(marker) {
		cut := limit
		for cut < len(marker) && cut > 0 && !utf8.RuneStart(marker[cut]) {
			cut--
		}
		return marker[:cut]
	}
	available := max(0, limit-len(marker))
	head := available * 3 / 4
	tail := len(text) - (available - head)
	for head > 0 && !utf8.RuneStart(text[head]) {
		head--
	}
	for tail < len(text) && !utf8.RuneStart(text[tail]) {
		tail++
	}
	return text[:head] + marker + text[tail:]
}
