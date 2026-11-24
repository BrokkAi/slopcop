package review

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

// copilotFake runs the fake agent as Copilot CLI with the account's models,
// a trailing "!" marking one Copilot lists but has not enabled.
func copilotFake(mode, models, trace string) []string {
	return append(fakeAdapter(mode, "", trace), "--fake-models="+models)
}

func copilotSettings(cmd []string) Settings {
	return Settings{Provider: ProviderCopilot, Adapters: map[string][]string{copilotAdapter: cmd}}
}

func readTrace(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestCopilotRouting(t *testing.T) {
	native := Settings{}
	copilot := Settings{Provider: ProviderCopilot}
	for _, f := range Families {
		if got := AdapterFor(f, native); got != AdapterName(f) {
			t.Errorf("AdapterFor(%s, native) = %s", f, got)
		}
		want := copilotAdapter
		if f == Flash {
			want = "dsh"
		}
		if got := AdapterFor(f, copilot); got != want {
			t.Errorf("AdapterFor(%s, copilot) = %s, want %s", f, got, want)
		}
	}
	if got := CommandFor(Haiku, copilot); !slices.Equal(got, copilotDefaultCommand) {
		t.Errorf("default Copilot command = %v", got)
	}
	copilot.Adapters = map[string][]string{copilotAdapter: {"copilot", "--acp"}, "claude": {"claude-acp"}}
	if got := CommandFor(Haiku, copilot); !slices.Equal(got, []string{"copilot", "--acp"}) {
		t.Errorf("configured Copilot command = %v", got)
	}
	if got := CommandFor(Flash, copilot); !slices.Equal(got, DefaultCommand(Flash)) {
		t.Errorf("flash under Copilot = %v", got)
	}
}

func TestCopilotArgsPutToolsLast(t *testing.T) {
	got := copilotArgs([]string{"copilot", "--acp", "--stdio"}, "gpt-6.2-sol", "high")
	want := []string{"copilot", "--acp", "--stdio", "--model", "gpt-6.2-sol", "--reasoning-effort", "high", "--available-tools", "view", "grep", "glob"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
	if got := copilotArgs([]string{"copilot"}, "", ""); !slices.Equal(got, []string{"copilot", "--available-tools", "view", "grep", "glob"}) {
		t.Fatalf("got %v", got)
	}
}

func TestCopilotErrorReply(t *testing.T) {
	for text, want := range map[string]bool{
		`Error: Execution failed: Model "claude-haiku-4.5" is not available.`: true,
		"Info: Operation cancelled by user":                                   true,
		CleanReply:                                                            false,
		"- [P1] Error: handling drops the wrapped cause":                      false,
		"Error: the change looks fine to me":                                  true,
		"The Error: prefix appears mid-line":                                  false,
	} {
		if got := copilotErrorReply(text); got != want {
			t.Errorf("copilotErrorReply(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestCopilotPick(t *testing.T) {
	opts := func(values ...string) []acp.SessionConfigSelectOption {
		var out []acp.SessionConfigSelectOption
		for _, v := range values {
			o := acp.SessionConfigSelectOption{Value: acp.SessionConfigValueId(v), Name: v}
			if name, ok := strings.CutSuffix(v, "!"); ok {
				o = acp.SessionConfigSelectOption{Value: acp.SessionConfigValueId(name), Name: name, Meta: map[string]any{"copilotEnablement": "disabled"}}
			}
			out = append(out, o)
		}
		return out
	}
	paid := opts("auto", "claude-haiku-4.5", "claude-haiku-5.5", "claude-sonnet-4.6", "claude-sonnet-5.5", "claude-opus-5.5", "claude-opus-5.5-fast", "gpt-5.6-sol", "gpt-6.2-sol", "gpt-6.1-terra", "gemini-3.8-flash")
	c := newCopilotCatalog(nil)
	// A fast mode is the same model at a higher price, so opus skips it.
	for f, want := range map[Family]string{Terra: "gpt-6.1-terra", Sol: "gpt-6.2-sol", Haiku: "claude-haiku-5.5", Sonnet: "claude-sonnet-5.5", Opus: "claude-opus-5.5"} {
		if got, auto, err := c.pick(f, paid); got != want || auto || err != nil {
			t.Errorf("pick(%s) = %q %v %v, want %s", f, got, auto, err, want)
		}
	}
	// A family Copilot lists but has not enabled does not land on Auto.
	if _, _, err := c.pick(Sol, opts("auto", "gpt-6.2-sol!")); err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Errorf("disabled sol: %v", err)
	}
	if _, _, err := c.pick(Haiku, opts("gpt-6.2-sol")); err == nil || !strings.Contains(err.Error(), "no haiku model") {
		t.Errorf("no haiku and no auto: %v", err)
	}
	free := opts("auto")
	if got, auto, err := c.pick(Terra, free); got != copilotAuto || !auto || err != nil {
		t.Fatalf("free terra = %q %v %v", got, auto, err)
	}
	// The same family keeps Auto for its retry; another family may not
	// share it.
	if _, auto, err := c.pick(Terra, free); !auto || err != nil {
		t.Errorf("terra retry: %v %v", auto, err)
	}
	if _, _, err := c.pick(Sol, free); err == nil || !strings.Contains(err.Error(), "only Auto") {
		t.Errorf("second Auto reviewer: %v", err)
	}
	// A failed Auto reviewer frees Auto for another family.
	c.release(Terra)
	if _, auto, err := c.pick(Sol, free); !auto || err != nil {
		t.Errorf("after release: %v %v", auto, err)
	}
	// Where the account has a named model, Auto could run that same
	// model, so a family without one does not fall back to it.
	if _, _, err := newCopilotCatalog(nil).pick(Haiku, opts("auto", "gpt-6.2-sol")); err == nil || !strings.Contains(err.Error(), "no haiku model") {
		t.Errorf("haiku beside sol: %v", err)
	}
}

func TestCopilotProbeRunsReadOnlyAtHighEffort(t *testing.T) {
	root, _, _ := reviewRepo(t)
	trace := filepath.Join(t.TempDir(), "trace")
	got := Probe(context.Background(), root, filepath.Join(t.TempDir(), "probe"), []Family{Sol, Haiku}, copilotSettings(copilotFake("copilot", "auto,gpt-6.2-sol,claude-haiku-5.5", trace)), nil)
	for i, model := range []string{"gpt-6.2-sol", "claude-haiku-5.5"} {
		r := got[i]
		if !r.Available || r.Model != model || r.Effort != "high" || r.Adapter != copilotAdapter || r.Warning != "" {
			t.Fatalf("%d: %+v", i, r)
		}
	}
	log := readTrace(t, trace)
	for _, want := range []string{
		"copilot model= effort= tools=view,grep,glob",
		"copilot model=gpt-6.2-sol effort=high tools=view,grep,glob",
		"copilot model=claude-haiku-5.5 effort=high tools=view,grep,glob",
		"close=fake",
	} {
		if !strings.Contains(log, want+"\n") {
			t.Errorf("trace lacks %q:\n%s", want, log)
		}
	}
	// One model listing serves both reviewers.
	if n := strings.Count(log, "copilot model= effort="); n != 1 {
		t.Errorf("listed models %d times:\n%s", n, log)
	}
}

func TestCopilotFallsBackToDefaultEffort(t *testing.T) {
	root, _, _ := reviewRepo(t)
	trace := filepath.Join(t.TempDir(), "trace")
	got := Probe(context.Background(), root, filepath.Join(t.TempDir(), "probe"), []Family{Haiku}, copilotSettings(copilotFake("copilot-no-effort", "auto,claude-haiku-4.5", trace)), nil)
	r := got[0]
	if !r.Available || r.Model != "claude-haiku-4.5" || r.Effort != copilotDefaultEffort || !strings.Contains(r.Warning, "default effort") || !strings.Contains(r.Warning, "does not support reasoning effort") {
		t.Fatalf("%+v", r)
	}
	log := readTrace(t, trace)
	if !strings.Contains(log, "copilot model=claude-haiku-4.5 effort=high") || !strings.Contains(log, "copilot model=claude-haiku-4.5 effort= tools=view,grep,glob") {
		t.Fatalf("trace:\n%s", log)
	}
}

func TestCopilotFreeRunsOneAutoReviewer(t *testing.T) {
	root, _, _ := reviewRepo(t)
	got := Probe(context.Background(), root, filepath.Join(t.TempDir(), "probe"), []Family{Terra, Sol}, copilotSettings(copilotFake("copilot", "auto,auto", "")), nil)
	if !got[0].Available || got[0].Model != copilotAuto || got[0].Effort != copilotDefaultEffort || !strings.Contains(got[0].Warning, "only Auto") || strings.Contains(got[0].Warning, "reasoning effort") {
		t.Fatalf("terra: %+v", got[0])
	}
	if got[1].Available || !strings.Contains(got[1].Reason, "already reviews on it") {
		t.Fatalf("sol: %+v", got[1])
	}
}

func TestCopilotTurnsOffAllowAll(t *testing.T) {
	root, _, _ := reviewRepo(t)
	trace := filepath.Join(t.TempDir(), "trace")
	got := Probe(context.Background(), root, filepath.Join(t.TempDir(), "probe"), []Family{Sol}, copilotSettings(copilotFake("copilot-allow-all", "gpt-6.2-sol", trace)), nil)
	if !got[0].Available {
		t.Fatalf("%+v", got[0])
	}
	if log := readTrace(t, trace); !strings.Contains(log, "set allow_all=off\n") {
		t.Fatalf("trace:\n%s", log)
	}
}

func TestCopilotErrorReplyFailsTheReviewer(t *testing.T) {
	root, c, store := reviewRepo(t)
	o := Options{Root: root, Settings: Settings{Reviewers: []Family{Haiku}, Provider: ProviderCopilot, Adapters: map[string][]string{copilotAdapter: copilotFake("copilot-error-reply", "claude-haiku-5.5", "")}}}
	v := Run(context.Background(), store, c, o)
	if v.Status != StatusFailed || len(v.Reviewers) != 1 {
		t.Fatalf("%+v", v)
	}
	r := v.Reviewers[0]
	if r.Status != StatusFailed || r.Adapter != copilotAdapter || !strings.Contains(r.Error, "instead of a review") || !strings.Contains(r.Error, "is not available") {
		t.Fatalf("%+v", r)
	}
}
