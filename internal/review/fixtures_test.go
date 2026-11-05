package review

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	}{{"openai/gpt-6.10-terra", Terra}, {"brokkai/deepseek-flash", Flash}, {"anthropic/claude-haiku-5-5", Haiku}, {"haiku", Haiku}, {"openai/gpt-6.1-sol", Sol}} {
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

func TestFamilyOfRejectsOtherModels(t *testing.T) {
	for _, m := range []string{"gpt-6-astra", "gpt-6-luna", "deepseek-v4-pro", "gpt-6-solar", "claude-haikuish"} {
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
