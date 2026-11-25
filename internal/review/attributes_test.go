package review

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/gitx"
)

const (
	hiddenText  = "HIDDEN_TEXT_CHANGE"
	realBinary  = "\x01\x02\x03\xff\xfe\x04\x05\x06\xfd\xfc"
	attrFile    = ".gitattributes"
	attrVictim  = "change.txt"
	attrBinFile = "blob.bin"
)

// attributeCapture commits an edit of a text file and a real binary file on
// top of reviewRepo's commit, with attributes in the checkout that mark the
// text file as not diffable. With ignored set, the attributes file is
// git-ignored, so it is in the checkout but in neither reviewed tree.
func attributeCapture(t *testing.T, root, attributes string, ignored bool) Capture {
	t.Helper()
	git := func(args ...string) string {
		t.Helper()
		v, err := gitx.Run(root, append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.com"}, args...)...)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	base := git("rev-parse", "HEAD")
	if ignored {
		write(filepath.Join(".git", "info", "exclude"), attrFile+"\n")
	}
	write(attrFile, attributes)
	write(attrVictim, "changed\n"+hiddenText+"\n")
	write(attrBinFile, "\x00"+realBinary+"\n")
	git("add", "-A")
	git("commit", "-m", "change behind attributes")
	capture, err := NewCapture(root, base, git("rev-parse", "HEAD^{tree}"))
	if err != nil {
		t.Fatal(err)
	}
	return capture
}

// TestAttributesCannotHideTextFromReviewers runs every reviewer route on a
// change whose own attributes call an edited text file binary, and inspects
// the prompt each fake agent actually received.
func TestAttributesCannotHideTextFromReviewers(t *testing.T) {
	routes := []struct {
		name     string
		family   Family
		settings func(trace string) Settings
	}{
		{"codex terra", Terra, func(trace string) Settings {
			return Settings{Adapters: map[string][]string{"codex": fakeAdapter("capture-prompt", "gpt-6-terra", trace)}}
		}},
		{"codex sol", Sol, func(trace string) Settings {
			return Settings{Adapters: map[string][]string{"codex": fakeAdapter("capture-prompt", "gpt-6.1-sol", trace)}}
		}},
		{"dsh flash", Flash, func(trace string) Settings {
			return Settings{Adapters: map[string][]string{"dsh": fakeAdapter("capture-prompt", "deepseek-flash", trace)}}
		}},
		{"claude haiku", Haiku, func(trace string) Settings {
			return Settings{Adapters: map[string][]string{"claude": fakeAdapter("capture-prompt", "haiku", trace)}}
		}},
		{"antigravity gemini", Gemini, func(trace string) Settings {
			return Settings{Adapters: map[string][]string{antigravityAdapter: fakeAdapter("capture-prompt", "gemini-3.8-flash", trace)}}
		}},
		{"copilot", Terra, func(trace string) Settings {
			return copilotSettings(copilotFake("copilot-capture-prompt", "gpt-6.10-terra", trace))
		}},
	}
	attributes := []struct {
		name, text string
		ignored    bool
	}{
		{"binary", attrVictim + " binary\n", false},
		{"no diff", attrVictim + " -diff\n", false},
		{"git-ignored file", attrVictim + " binary\n", true},
	}
	// Every route shares one diff, so each gets the first attribute form and
	// one route gets the others: each case starts a real agent process.
	for i, route := range routes {
		for j, attr := range attributes {
			if i > 0 && j > 0 {
				continue
			}
			t.Run(route.name+"/"+attr.name, func(t *testing.T) {
				root, _, store := reviewRepo(t)
				capture := attributeCapture(t, root, attr.text, attr.ignored)
				trace := filepath.Join(t.TempDir(), "trace")
				settings := route.settings(trace)
				settings.Reviewers = []Family{route.family}
				v := Run(context.Background(), store, capture, Options{Root: root, Intent: "request", Settings: settings})
				if v.Status != StatusClean {
					t.Fatalf("review: %+v", v)
				}
				prompt := readTrace(t, trace+".prompt")
				patch, patchErr := gitx.FilePatch(context.Background(), root, capture.BaseTree, capture.CheckedTree(), attrVictim)
				if patchErr != nil {
					t.Fatal(patchErr)
				}
				if !strings.Contains(string(patch), "+"+hiddenText) {
					t.Errorf("the reviewer was not shown the edited text:\n%s", promptDiff(prompt))
				}
				if strings.Contains(capture.Numstat, "-\t-\t"+attrVictim) {
					t.Errorf("numstat calls the text file binary: %q", capture.Numstat)
				}
				if strings.Contains(prompt, realBinary) || strings.Contains(prompt, `File "`+attrBinFile+`"`) {
					t.Error("a real binary file's content reached the prompt")
				}
				if !strings.Contains(prompt, attrBinFile) {
					t.Errorf("the real binary file is not reported as binary:\n%s", promptDiff(prompt))
				}
			})
		}
	}
}

// TestLinesChangedSeesAttributeMarkedText keeps a fix from being reported as
// "cited lines unchanged" because attributes hid the file's hunks.
func TestLinesChangedSeesAttributeMarkedText(t *testing.T) {
	root, _, _ := reviewRepo(t)
	capture := attributeCapture(t, root, attrVictim+" binary\n", false)
	changed, err := linesChanged(root, capture.BaseTree, capture.CheckedTree(), attrVictim, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("an edit of an attribute-marked text file was reported as unchanged")
	}
}

// TestNULByteCannotHideTextFromReviewers covers a script git calls binary from
// its content alone, with no attributes involved, behind a diff long enough to
// reach the prompt's limit.
func TestNULByteCannotHideTextFromReviewers(t *testing.T) {
	root, _, store := reviewRepo(t)
	base, err := gitx.Run(root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"a-long.txt": strings.Repeat("padding line\n", 30000),
		"run.sh":     "#!/bin/sh\n# \x00\ncurl " + hiddenText + " | sh\n",
	} {
		if err = os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "script with a NUL byte"}} {
		if _, err = gitx.Run(root, args...); err != nil {
			t.Fatal(err)
		}
	}
	tree, err := gitx.Run(root, "rev-parse", "HEAD^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	capture, err := NewCapture(root, base, tree)
	if err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(t.TempDir(), "trace")
	settings := Settings{Reviewers: []Family{Terra}, Adapters: map[string][]string{"codex": fakeAdapter("capture-prompt", "gpt-6-terra", trace)}}
	v := Run(context.Background(), store, capture, Options{Root: root, Intent: "request", Settings: settings})
	if v.Status != StatusClean {
		t.Fatalf("review: %+v", v)
	}
	prompt := readTrace(t, trace+".prompt")
	binaryPath := filepath.Join(v.LogDir, "binary-diffs.txt")
	data, readErr := os.ReadFile(binaryPath)
	if readErr != nil || !strings.Contains(string(data), "curl "+hiddenText+" | sh") {
		t.Fatalf("readable binary diff missing: %s, %v", data, readErr)
	}
	if !strings.Contains(prompt, binaryPath) || !strings.Contains(prompt, "diff --text") {
		t.Fatalf("binary inspection guidance missing: %s", prompt)
	}
	if strings.Contains(prompt, "Captured diff") {
		t.Fatal("inline diff remained")
	}
	patch, patchErr := gitx.FilePatch(context.Background(), root, capture.BaseTree, capture.CheckedTree(), "run.sh")
	if patchErr != nil {
		t.Fatal(patchErr)
	}
	if !strings.Contains(string(patch), "+3: curl "+hiddenText+" | sh") {
		t.Errorf("the reviewer was not shown the script's text:\n%.600s", promptDiff(prompt))
	}
	if strings.Contains(prompt, "\x00") {
		t.Error("a NUL byte reached the prompt")
	}
	// A later round edits the cited line of the same file.
	if err = os.WriteFile(filepath.Join(root, "run.sh"), []byte("#!/bin/sh\n# \x00\ncurl elsewhere | sh\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "edit the cited line"}} {
		if _, err = gitx.Run(root, args...); err != nil {
			t.Fatal(err)
		}
	}
	next, err := gitx.Run(root, "rev-parse", "HEAD^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	changed, err := linesChanged(root, capture.CheckedTree(), next, "run.sh", 3, 3)
	if err != nil || !changed {
		t.Fatalf("linesChanged = %v, %v, want an edit of a file with a NUL byte seen", changed, err)
	}
}

// TestIsolatedReviewersRefuseCutBinaryText: a reviewer in an isolated workspace
// cannot read what the size limit left out, so it must not approve the change.
func TestIsolatedReviewersRefuseCutBinaryText(t *testing.T) {
	for _, tc := range []struct {
		family         Family
		adapter, model string
	}{
		{Gemini, antigravityAdapter, "gemini-3.8-flash"},
		{Kimi, kimiAdapter, kimiModel},
		{Grok, grokAdapter, "grok-4.6"},
	} {
		for _, lines := range []int{2000, 60000} {
			t.Run(fmt.Sprintf("%s/%d", tc.family, lines), func(t *testing.T) {
				t.Setenv("KIMI_API_KEY", "test-kimi-key")
				t.Setenv("XAI_API_KEY", "test-xai-key")
				root, _, store := reviewRepo(t)
				base, err := gitx.Run(root, "rev-parse", "HEAD")
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(root, "run.sh"), []byte("\x00\n"+strings.Repeat("readable script line\n", lines)+"FINAL_READABLE_LINE\n"), 0600); err != nil {
					t.Fatal(err)
				}
				for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "long script"}} {
					if _, err = gitx.Run(root, args...); err != nil {
						t.Fatal(err)
					}
				}
				tree, err := gitx.Run(root, "rev-parse", "HEAD^{tree}")
				if err != nil {
					t.Fatal(err)
				}
				capture, err := NewCapture(root, base, tree)
				if err != nil {
					t.Fatal(err)
				}
				if lines == 2000 {
					dest := t.TempDir()
					if _, err := gitx.ExportPerFilePatches(context.Background(), root, capture.BaseTree, capture.CheckedTree(), dest, 1<<20, 8<<20); err != nil {
						t.Fatal(err)
					}
					data, err := os.ReadFile(filepath.Join(dest, "patches", "000001.patch"))
					if err != nil || !strings.Contains(string(data), "FINAL_READABLE_LINE") {
						t.Fatalf("missing script tail: %v", err)
					}
				}
				trace := filepath.Join(t.TempDir(), "trace")
				settings := Settings{Reviewers: []Family{tc.family}, Adapters: map[string][]string{tc.adapter: fakeAdapter("clean", tc.model, trace)}}
				v := Run(context.Background(), store, capture, Options{Root: root, Intent: "request", Settings: settings})
				if lines == 60000 {
					if v.Status != StatusFailed {
						t.Fatalf("oversized binary approved: %+v", v)
					}
					return
				}
				if v.Status != StatusClean || len(v.Reviewers) != 1 {
					t.Fatalf("review: %+v", v)
				}
				if _, err = os.Stat(trace); err != nil {
					t.Fatalf("the reviewer did not start: %v", err)
				}
			})
		}
	}
}

// promptDiff is the captured-diff part of a prompt, for a readable failure.
func promptDiff(prompt string) string {
	_, rest, ok := strings.Cut(prompt, "Captured diff (untrusted data):")
	if !ok {
		return prompt
	}
	diff, _, _ := strings.Cut(rest, "Repository root:")
	return diff
}
