package report

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/BrokkAi/slopcop/internal/reposize"
	"github.com/BrokkAi/slopcop/internal/service"
)

// enterpriseLine is the other way past the checkout bounds.
const enterpriseLine = "Or talk to us about an enterprise agreement for higher limits.\n"

// SizeAdvice says what takes up the space in a repository near or over the
// checkout size limit and how to leave it out of checks. size is the
// service's measure when the check ran, nil for a repository_too_large
// refusal, whose own message already says it is over. breakdown is the local
// measure, nil when it could not be taken.
func SizeAdvice(size *service.RepositorySize, breakdown *reposize.Breakdown) string {
	var out strings.Builder
	if size != nil {
		fmt.Fprintf(&out,
			"Repository near the size limit: the check's checkout held %s of %s files and %s of %s.\n",
			count(size.Files), count(size.MaxFiles), Bytes(size.Bytes), Bytes(size.MaxBytes),
		)
	}
	if breakdown == nil {
		out.WriteString("Keep generated, vendored or data paths out of every check with the [check] exclude list in .slopcop/config.toml.\n")
		out.WriteString(enterpriseLine)
		return out.String()
	}
	if breakdown.ConfigError != "" {
		fmt.Fprintf(&out, "Note: %s, so its [check] exclusions are counted below.\n", printable(breakdown.ConfigError))
	}
	fmt.Fprintf(&out, "Base tree %s: %s file(s), %s",
		shortCommit(breakdown.Base), count(int64(breakdown.Files)), Bytes(breakdown.Bytes))
	if breakdown.ExcludedFiles > 0 {
		fmt.Fprintf(&out, " after .bifrostignore and [check] exclude leave out %s file(s), %s",
			count(int64(breakdown.ExcludedFiles)), Bytes(breakdown.ExcludedBytes))
	}
	by := "size"
	if breakdown.RankedBy == reposize.RankedByFiles {
		by = "file count"
	}
	fmt.Fprintf(&out, ". Largest paths by %s:\n", by)
	width := 0
	names := make([]string, len(breakdown.Largest))
	for i, entry := range breakdown.Largest {
		names[i] = printable(entry.Path)
		if entry.Directory {
			names[i] += "/"
		}
		width = max(width, utf8.RuneCountInString(names[i]))
	}
	for i, entry := range breakdown.Largest {
		fmt.Fprintf(&out, "  %s%s  %10s  %s file(s)\n",
			names[i], strings.Repeat(" ", width-utf8.RuneCountInString(names[i])),
			Bytes(entry.Bytes), count(int64(entry.Files)))
	}
	if len(breakdown.Largest) == 0 {
		out.WriteString(enterpriseLine)
		return out.String()
	}
	patterns := append(append([]string{}, breakdown.Exclude...), breakdown.Largest[0].Pattern)
	quoted := make([]string, len(patterns))
	for i, pattern := range patterns {
		quoted[i] = tomlString(pattern)
	}
	exclude := "exclude = [" + strings.Join(quoted, ", ") + "]"
	if breakdown.HasCheck {
		fmt.Fprintf(&out, "Keep the generated, vendored or data paths among them out of every check in the [check] section of .slopcop/config.toml, for example:\n  %s\n", exclude)
	} else {
		fmt.Fprintf(&out, "Keep the generated, vendored or data paths among them out of every check in .slopcop/config.toml, for example:\n  [check]\n  %s\n", exclude)
	}
	out.WriteString(enterpriseLine)
	return out.String()
}

// tomlString quotes value as a TOML basic string.
func tomlString(value string) string {
	var out strings.Builder
	out.WriteByte('"')
	for _, r := range value {
		switch {
		case r == '"' || r == '\\':
			out.WriteByte('\\')
			out.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&out, `\u%04X`, r)
		default:
			out.WriteRune(r)
		}
	}
	out.WriteByte('"')
	return out.String()
}

// Bytes is a size in binary units, as the service's limits are stated.
func Bytes(value int64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	size, suffix := float64(value)/unit, "KiB"
	for _, next := range []string{"MiB", "GiB", "TiB"} {
		if size < unit {
			break
		}
		size, suffix = size/unit, next
	}
	return fmt.Sprintf("%.1f %s", size, suffix)
}

// count is n with thousands separators.
func count(n int64) string {
	digits := strconv.FormatInt(n, 10)
	sign := ""
	if strings.HasPrefix(digits, "-") {
		sign, digits = "-", digits[1:]
	}
	var out strings.Builder
	for i, digit := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			out.WriteByte(',')
		}
		out.WriteRune(digit)
	}
	return sign + out.String()
}

func shortCommit(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}
