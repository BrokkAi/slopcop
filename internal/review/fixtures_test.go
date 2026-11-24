package review

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

func TestAdapterConfigFixtures(t *testing.T) {
	for _, tc := range []struct {
		file   string
		family Family
		model  string
	}{{"codex-2.5-options.json", Terra, "gpt-6.10-terra"}, {"dsh-0.6-options.json", Flash, "deepseek-flash"}, {"mj-codex-options.json", Terra, "gpt-6.10-terra"}} {
		t.Run(tc.file, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", tc.file))
			if err != nil {
				t.Fatal(err)
			}
			var options []acp.SessionConfigOption
			if err = json.Unmarshal(data, &options); err != nil {
				t.Fatal(err)
			}
			model := configOption(options, "model")
			got, ok := bestModel(tc.family, choices(model))
			if !ok || got != tc.model {
				t.Fatalf("model %q want %q", got, tc.model)
			}
			effort := configOption(options, "effort")
			found := false
			for _, choice := range choices(effort) {
				if choice.Value == "high" {
					found = true
				}
			}
			if !found {
				t.Fatal("high effort missing")
			}
			key, value := reviewerMode(tc.family)
			if !hasChoice(configOption(options, key), value) {
				t.Fatalf("mode %s=%s missing", key, value)
			}
		})
	}
}
func TestNamespacedFamily(t *testing.T) {
	for _, tc := range []struct {
		model  string
		family Family
	}{{"openai/gpt-6.10-terra", Terra}, {"brokkai/deepseek-flash", Flash}, {"anthropic/claude-haiku-5-5", Haiku}, {"haiku", Haiku}, {"openai/gpt-6.1-sol", Sol}, {"sonnet", Sonnet}, {"anthropic/claude-sonnet-5-5", Sonnet}, {"opus", Opus}, {"claude-opus-5.5", Opus}} {
		family, ok := FamilyOf(tc.model)
		if !ok || family != tc.family {
			t.Fatalf("%q: %q", tc.model, family)
		}
	}
}

func TestBestModelUsesDisplayVersion(t *testing.T) {
	terra := []acp.SessionConfigSelectOption{
		{Value: "gpt-6.1-terra", Name: "Terra 6.10"},
		{Value: "gpt-5.6-terra", Name: "Terra 5.6"},
	}
	if got, ok := bestModel(Terra, terra); !ok || got != "gpt-6.1-terra" {
		t.Fatalf("Terra model %q, want gpt-6.1-terra", got)
	}
	options := []acp.SessionConfigSelectOption{
		{Value: "claude-haiku-4-5-20251001", Name: "Haiku 4.5"},
		{Value: "haiku", Name: "Haiku 5.5"},
	}
	got, ok := bestModel(Haiku, options)
	if !ok || got != "haiku" {
		t.Fatalf("model %q, want haiku", got)
	}
	options = []acp.SessionConfigSelectOption{
		{Value: "haiku", Name: "Haiku 5.5"},
		{Value: "claude-haiku-5-5-20251001", Name: "Haiku 5.5"},
	}
	got, ok = bestModel(Haiku, options)
	if !ok || got != "claude-haiku-5-5-20251001" {
		t.Fatalf("model %q, want explicit id", got)
	}
}

// TestBestClaudeModel uses the model list Claude ACP 0.100.0 advertises: each
// Claude family takes its own alias, and none takes the default or another
// family's model.
func TestBestClaudeModel(t *testing.T) {
	options := []acp.SessionConfigSelectOption{
		{Value: "default", Name: "Default (recommended)"},
		{Value: "opus", Name: "Opus 5.5"},
		{Value: "claude-fable-5-2[1m]", Name: "Fable 5.2"},
		{Value: "sonnet", Name: "Sonnet 5.5"},
		{Value: "haiku", Name: "Haiku 5.5"},
	}
	for f, want := range map[Family]string{Haiku: "haiku", Sonnet: "sonnet", Opus: "opus"} {
		if got, ok := bestModel(f, options); !ok || got != want {
			t.Errorf("%s: model %q, want %s", f, got, want)
		}
		if AdapterName(f) != "claude" || !slices.Equal(DefaultCommand(f), DefaultCommand(Haiku)) {
			t.Errorf("%s does not run on Haiku's adapter", f)
		}
	}
	options = []acp.SessionConfigSelectOption{
		{Value: "claude-opus-4-6", Name: "Opus 4.6"},
		{Value: "claude-opus-5-5", Name: "Opus 5.5"},
		{Value: "claude-sonnet-5-5", Name: "Sonnet 5.5"},
	}
	if got, ok := bestModel(Opus, options); !ok || got != "claude-opus-5-5" {
		t.Fatalf("opus: model %q, want the newest", got)
	}
}

func TestFamilyOfRejectsOtherModels(t *testing.T) {
	for _, m := range []string{"gpt-6-astra", "gpt-6-luna", "deepseek-v4-pro", "gpt-6-solar", "claude-haikuish", "claude-sonnetish", "opus-5", "claude-opus-4.6-fast", "claude-sonnet-5.5-fast"} {
		if f, ok := FamilyOf(m); ok {
			t.Fatalf("%s: %s", m, f)
		}
	}
}

func TestReviewerModeChoices(t *testing.T) {
	for _, tc := range []struct {
		family     Family
		key, value string
		options    string
	}{
		{Terra, "mode", "read-only", `[{"id":"mode","name":"Mode","type":"select","currentValue":"default","options":[{"value":"read-only","name":"Read only"}]}]`},
		{Haiku, "mode", "default", `[{"id":"mode","name":"Mode","type":"select","currentValue":"auto","options":[{"value":"default","name":"Manual"}]}]`},
		{Sonnet, "mode", "default", `[{"id":"mode","name":"Mode","type":"select","currentValue":"auto","options":[{"value":"default","name":"Manual"}]}]`},
		{Opus, "mode", "default", `[{"id":"mode","name":"Mode","type":"select","currentValue":"auto","options":[{"value":"default","name":"Manual"}]}]`},
		{Flash, "permission_mode", "ask", `[{"id":"permission_mode","name":"Permissions","type":"select","currentValue":"allow","options":[{"value":"ask","name":"Ask"}]}]`},
	} {
		var opts []acp.SessionConfigOption
		if err := json.Unmarshal([]byte(tc.options), &opts); err != nil {
			t.Fatal(err)
		}
		key, value := reviewerMode(tc.family)
		if key != tc.key || value != tc.value || !hasChoice(configOption(opts, key), value) {
			t.Fatalf("mode %s=%s unavailable", key, value)
		}
		if hasChoice(configOption(nil, key), value) {
			t.Fatal("missing mode selected")
		}
	}
}
