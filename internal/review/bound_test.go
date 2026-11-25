package review

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBoundReviewSection(t *testing.T) {
	if got := boundReviewSection("short", 100, "test"); got != "short" {
		t.Fatal(got)
	}
	text := "HEAD" + strings.Repeat("界é", 1000) + "TAIL"
	got := boundReviewSection(text, 200, "changed files")
	if len(got) > 200 || !utf8.ValidString(got) || !strings.HasPrefix(got, "HEAD") || !strings.HasSuffix(got, "TAIL") || !strings.Contains(got, "\n…[changed files omitted]…\n") {
		t.Fatal(got)
	}
}

func TestBoundReviewSectionSmallLimits(t *testing.T) {
	marker := "\n…[test omitted]…\n"
	for limit := -1; limit <= len(marker); limit++ {
		for _, text := range []string{"", strings.Repeat("界", 100)} {
			got := boundReviewSection(text, limit, "test")
			if len(got) > max(limit, 0) || !utf8.ValidString(got) {
				t.Fatalf("limit %d: %q", limit, got)
			}
			if len(text) > limit && limit > 0 {
				if !strings.HasPrefix(marker, got) {
					t.Fatalf("not a marker prefix: %q", got)
				}
				if len(got) < len(marker) && len(got)+len(string([]rune(marker[len(got):])[0])) <= limit {
					t.Fatalf("prefix is not longest: %q", got)
				}
			}
		}
	}
}
