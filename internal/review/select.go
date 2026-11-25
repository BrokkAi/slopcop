package review

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	acp "github.com/coder/acp-go-sdk"
)

// DefaultCommand returns the pinned ACP adapter argv for a reviewer family.
func DefaultCommand(f Family) []string {
	switch f {
	case Terra, Sol:
		return []string{"npx", "--yes", "@agentclientprotocol/codex-acp@2.5.0"}
	case Flash:
		return []string{"npx", "--yes", "@brokkai/dsh-acp@0.6.0"}
	case Haiku, Sonnet, Opus:
		return []string{"npx", "--yes", "@agentclientprotocol/claude-agent-acp@0.100.0"}
	case Gemini:
		return slices.Clone(antigravityDefaultCommand)
	case Kimi:
		return slices.Clone(kimiDefaultCommand)
	case Grok:
		return slices.Clone(grokDefaultCommand)
	case Ollama:
		return slices.Clone(ollamaDefaultCommand)
	case OpenRouter:
		return []string{chatSelfCommand, "review-agent", "--provider=" + string(f)}
	}
	return nil
}

// AdapterName identifies the ACP adapter used by a family.
func AdapterName(f Family) string {
	switch f {
	case Terra, Sol:
		return "codex"
	case Flash:
		return dshAdapter
	case Haiku, Sonnet, Opus:
		return "claude"
	case Gemini:
		return antigravityAdapter
	case Kimi:
		return kimiAdapter
	case Grok:
		return grokAdapter
	case Ollama:
		return ollamaAdapter
	case OpenRouter:
		return string(f)
	}
	return ""
}

// AdapterFor names the adapter that runs a family under these settings:
// Copilot for the families it serves when the provider is Copilot, otherwise
// the family's own adapter.
func AdapterFor(f Family, s Settings) string {
	if s.Provider == ProviderCopilot && CopilotServes(f) {
		return copilotAdapter
	}
	return AdapterName(f)
}

// CommandFor returns the argv that starts a family's adapter: the configured
// override, else the pinned package, with dsh's key file when one is set.
func CommandFor(f Family, s Settings) []string {
	adapter := AdapterFor(f, s)
	if IsChatAdapter(adapter) {
		return chatCommand(f, s)
	}
	if cmd := s.Adapters[adapter]; len(cmd) > 0 {
		return cmd
	}
	if adapter == copilotAdapter {
		return slices.Clone(copilotDefaultCommand)
	}
	if adapter == dshAdapter && s.DSHKeyFile != "" {
		return append(DefaultCommand(f), "--api-key-file", s.DSHKeyFile)
	}
	return DefaultCommand(f)
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
	case strings.HasPrefix(m, "deepseek-") && has("flash"):
		return Flash, true
	case m == "haiku" || strings.HasPrefix(m, "claude-") && has("haiku"):
		return Haiku, true
	// Copilot lists a fast mode of some Claude models under its own id. It is
	// the same model at a higher price, so it is left out.
	case m == "sonnet" || strings.HasPrefix(m, "claude-") && has("sonnet") && !has("fast"):
		return Sonnet, true
	case m == "opus" || strings.HasPrefix(m, "claude-") && has("opus") && !has("fast"):
		return Opus, true
	case strings.HasPrefix(m, "gemini-") && (has("flash") || has("pro")) && !has("lite") && !has("image"):
		return Gemini, true
	case kimiModelName.MatchString(m):
		return Kimi, true
	case grokModel.MatchString(m):
		return Grok, true
	}
	return "", false
}

// grokModel matches plain versioned Grok models and grok-build. Dated variants
// such as grok-4.20-0309-reasoning are left out: their numbers would sort them
// above newer releases, and some are non-reasoning or multi-agent.
var grokModel = regexp.MustCompile(`^grok-([0-9]+(\.[0-9]+)?|build)$`)

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
	// Grok has no mode option; its profile and sandbox restrict it instead.
	if f == Grok {
		return "", ""
	}
	if f == Flash {
		return "permission_mode", "ask"
	}
	// Antigravity's default mode asks before edits and commands; its plan
	// mode only adds an instruction, and its auto mode approves them.
	// Kimi's default mode asks before anything outside its approved tools.
	if AdapterName(f) == "claude" || f == Gemini || f == Kimi {
		return "mode", "default"
	}
	return "mode", "read-only"
}

func hasChoice(option *acp.SessionConfigOptionSelect, value string) bool {
	for _, choice := range choices(option) {
		if string(choice.Value) == value {
			return true
		}
	}
	return false
}
