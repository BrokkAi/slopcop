package review

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	acp "github.com/coder/acp-go-sdk"
)

// DefaultCommand returns the pinned ACP adapter argv for a reviewer family.
func DefaultCommand(f Family) []string {
	switch f {
	case Terra, Sol:
		return []string{"npx", "--yes", "@agentclientprotocol/codex-acp@2.5.0"}
	case DeepSeek, Flash:
		return []string{"npx", "--yes", "@brokkai/dsh-acp@0.6.0"}
	case Sonnet, Haiku:
		return []string{"npx", "--yes", "@agentclientprotocol/claude-agent-acp@0.100.0"}
	}
	return nil
}

// AdapterName identifies the ACP adapter used by a family.
func AdapterName(f Family) string {
	switch f {
	case Terra, Sol:
		return "codex"
	case DeepSeek, Flash:
		return "dsh"
	case Sonnet, Haiku:
		return "claude"
	}
	return ""
}

// ParseFamily accepts only the supported reviewer families.
func ParseFamily(s string) (Family, error) {
	f := Family(s)
	if DefaultCommand(f) == nil {
		return "", fmt.Errorf("unknown review family %q", s)
	}
	return f, nil
}

// FamilyOf classifies model ids, including provider qualified ids.
func FamilyOf(model string) (Family, bool) {
	m := strings.ToLower(model)
	if i := strings.LastIndexByte(m, '/'); i >= 0 {
		m = m[i+1:]
	}
	tokens := strings.Split(m, "-")
	has := func(token string) bool {
		for _, part := range tokens[1:] {
			if part == token {
				return true
			}
		}
		return false
	}
	switch {
	case strings.HasPrefix(m, "gpt-") && has("terra"):
		return Terra, true
	case strings.HasPrefix(m, "gpt-") && has("sol"):
		return Sol, true
	case m == "sonnet" || strings.HasPrefix(m, "claude-") && has("sonnet"):
		return Sonnet, true
	case strings.HasPrefix(m, "deepseek-") && has("pro"):
		return DeepSeek, true
	case strings.HasPrefix(m, "deepseek-") && has("flash"):
		return Flash, true
	case m == "haiku" || strings.HasPrefix(m, "claude-") && has("haiku"):
		return Haiku, true
	}
	return "", false
}

// configOption locates grouped or ungrouped ACP selectors by id or category.
func configOption(options []acp.SessionConfigOption, key string) *acp.SessionConfigOptionSelect {
	for _, o := range options {
		if o.Select != nil && string(o.Select.Id) == key {
			return o.Select
		}
	}
	for _, o := range options {
		if o.Select == nil {
			continue
		}
		cat := ""
		if o.Select.Category != nil {
			cat = string(*o.Select.Category)
		}
		if key == "model" && cat == "model" {
			return o.Select
		}
		if key == "effort" && (cat == "thought_level" || string(o.Select.Id) == "reasoning_effort") {
			return o.Select
		}
	}
	return nil
}

// choices flattens both ACP select option layouts.
func choices(o *acp.SessionConfigOptionSelect) []acp.SessionConfigSelectOption {
	if o == nil {
		return nil
	}
	if o.Options.Ungrouped != nil {
		return []acp.SessionConfigSelectOption(*o.Options.Ungrouped)
	}
	var out []acp.SessionConfigSelectOption
	if o.Options.Grouped != nil {
		for _, g := range *o.Options.Grouped {
			out = append(out, g.Options...)
		}
	}
	return out
}

var nums = regexp.MustCompile(`[0-9]+`)

// compareVersions compares numeric model versions instead of lexical strings.
func compareVersions(a, b string) int {
	aa, bb := nums.FindAllString(a, -1), nums.FindAllString(b, -1)
	for i := 0; i < len(aa) || i < len(bb); i++ {
		var x, y int
		if i < len(aa) {
			x, _ = strconv.Atoi(aa[i])
		}
		if i < len(bb) {
			y, _ = strconv.Atoi(bb[i])
		}
		if x != y {
			if x > y {
				return 1
			}
			return -1
		}
	}
	return 0
}

// bestModel selects the newest advertised model in the requested family.
func bestModel(f Family, options []acp.SessionConfigSelectOption) (string, bool) {
	best := ""
	bestVersion := ""
	for _, o := range options {
		v := string(o.Value)
		match, ok := FamilyOf(v)
		if !ok && f == Haiku && v == "haiku" {
			ok = true
			match = Haiku
		}
		if !ok || match != f {
			continue
		}
		version := v
		if nums.MatchString(o.Name) {
			version = o.Name
		}
		cmp := compareVersions(version, bestVersion)
		if best == "" || cmp > 0 || cmp == 0 && (nums.MatchString(v) && !nums.MatchString(best) || nums.MatchString(v) == nums.MatchString(best) && v > best) {
			best = v
			bestVersion = version
		}
	}
	return best, best != ""
}

func reviewerMode(f Family) (string, string) {
	if AdapterName(f) == "dsh" {
		return "permission_mode", "ask"
	}
	if AdapterName(f) == "claude" {
		return "mode", "default"
	}
	return "mode", "read-only"
}

// defaultEfforts matches Mjolnir's two-reviewer presets. Sol uses medium in
// the Terra/Sol pair; the repeated-family fallback uses high for both slots.
func defaultEfforts(reviewers []Family) map[Family]string {
	efforts := map[Family]string{}
	for _, f := range reviewers {
		efforts[f] = "high"
	}
	if len(reviewers) == 2 && reviewers[0] == Terra && reviewers[1] == Sol {
		efforts[Sol] = "medium"
	}
	return efforts
}

// bestEffort uses the requested effort, the closest offered effort below it,
// or the lowest offered effort if every choice is higher (as in Mjolnir).
func bestEffort(requested string, options []acp.SessionConfigSelectOption) string {
	levels := []string{"minimal", "low", "medium", "high", "xhigh", "max"}
	available := map[string]bool{}
	for _, o := range options {
		available[string(o.Value)] = true
	}
	if available[requested] {
		return requested
	}
	below := ""
	for _, level := range levels {
		if level == requested {
			break
		}
		if available[level] {
			below = level
		}
	}
	if below != "" {
		return below
	}
	for _, level := range levels {
		if available[level] {
			return level
		}
	}
	return ""
}

func hasChoice(option *acp.SessionConfigOptionSelect, value string) bool {
	for _, choice := range choices(option) {
		if string(choice.Value) == value {
			return true
		}
	}
	return false
}
