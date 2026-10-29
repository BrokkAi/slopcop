package hooks

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	return home
}

func writeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
}

func readConfig(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestInstallIsIdempotentAndRemoveUndoesIt(t *testing.T) {
	home := isolateHome(t)
	path := filepath.Join(home, ".claude", "settings.json")

	result, err := Install()
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ChangedPaths) != 1 || result.ChangedPaths[0] != path {
		t.Fatalf("result = %+v", result)
	}
	data := readConfig(t, path)
	if bytes.Count(data, []byte(`"command": "slopcop hook claude || true"`)) != 1 || !bytes.Contains(data, []byte(`"timeout": 720`)) {
		t.Fatalf("settings.json =\n%s", data)
	}

	result, err = Install()
	if err != nil || len(result.ChangedPaths) != 0 {
		t.Fatalf("second install = %+v, %v", result, err)
	}
	if !bytes.Equal(data, readConfig(t, path)) {
		t.Fatal("second install rewrote settings.json")
	}

	result, err = Remove()
	if err != nil || len(result.ChangedPaths) != 1 {
		t.Fatalf("remove = %+v, %v", result, err)
	}
	if got := string(readConfig(t, path)); got != "{}\n" {
		t.Fatalf("remove left %q", got)
	}
	result, err = Remove()
	if err != nil || len(result.ChangedPaths) != 0 {
		t.Fatalf("second remove = %+v, %v", result, err)
	}
}

func TestInstallAndRemovePreserveUserConfiguration(t *testing.T) {
	home := isolateHome(t)
	path := filepath.Join(home, ".claude", "settings.json")
	writeConfig(t, path, `{
  "theme": "dark",
  "hooks": {
    "Stop": [
      {"matcher": "mine", "hooks": [
        {"type": "command", "command": "my-stop"},
        {"type": "command", "command": "slopcop hook claude", "timeout": 1},
        {"type": "command", "command": "slopcop hook claude"}
      ]},
      {"matcher": "only-slopcop", "hooks": [{"type": "command", "command": "slopcop hook claude"}]}
    ],
    "PostToolUse": [{"hooks": [{"type": "command", "command": "slopcop hook claude"}]}]
  },
  "unknown": {"keep": 12345678901234567890}
}
`)

	if _, err := Install(); err != nil {
		t.Fatal(err)
	}
	installed := readConfig(t, path)
	// The legacy handlers under Stop become one guarded handler; the one
	// under PostToolUse is not SlopCop's Stop hook and stays.
	if bytes.Count(installed, []byte(`"command": "slopcop hook claude || true"`)) != 1 ||
		bytes.Count(installed, []byte(`"command": "slopcop hook claude"`)) != 1 {
		t.Fatalf("Stop dedup or unrelated event preservation failed:\n%s", installed)
	}
	for _, want := range []string{`"theme": "dark"`, `"command": "my-stop"`, `"matcher": "mine"`, `"keep": 12345678901234567890`} {
		if !bytes.Contains(installed, []byte(want)) {
			t.Errorf("installed settings lack %s", want)
		}
	}
	if bytes.Contains(installed, []byte("only-slopcop")) {
		t.Error("a group made empty by deduplication was retained")
	}

	if _, err := Remove(); err != nil {
		t.Fatal(err)
	}
	removed := readConfig(t, path)
	if bytes.Contains(removed, []byte(`"slopcop hook claude || true"`)) || bytes.Count(removed, []byte(`"command": "slopcop hook claude"`)) != 1 {
		t.Fatalf("remove did not limit itself to Stop:\n%s", removed)
	}
	for _, want := range []string{`"theme": "dark"`, `"command": "my-stop"`, `"PostToolUse"`, `"keep": 12345678901234567890`} {
		if !bytes.Contains(removed, []byte(want)) {
			t.Errorf("removed settings lack %s", want)
		}
	}
}

func TestInstallKeepsKeyOrderAndText(t *testing.T) {
	home := isolateHome(t)
	path := filepath.Join(home, ".claude", "settings.json")
	writeConfig(t, path, `{"zeta": 1, "statusLine": {"command": "a && b > c <d>"}, "alpha": [true, null, 1.50]}`)

	if _, err := Install(); err != nil {
		t.Fatal(err)
	}
	if _, err := Remove(); err != nil {
		t.Fatal(err)
	}
	want := `{
  "zeta": 1,
  "statusLine": {
    "command": "a && b > c <d>"
  },
  "alpha": [
    true,
    null,
    1.50
  ]
}
`
	if got := string(readConfig(t, path)); got != want {
		t.Fatalf("settings.json =\n%s\nwant\n%s", got, want)
	}
}

func TestNullOrEmptySettingsAreAnEmptyObject(t *testing.T) {
	home := isolateHome(t)
	path := filepath.Join(home, ".claude", "settings.json")
	for _, content := range []string{"null", "", "  \n"} {
		writeConfig(t, path, content)
		if _, err := Install(); err != nil {
			t.Fatalf("Install of %q: %v", content, err)
		}
		if !bytes.Contains(readConfig(t, path), []byte(`"command": "slopcop hook claude || true"`)) {
			t.Fatalf("Install of %q did not add the hook", content)
		}
	}
}

func TestMalformedSettingsAreNotOverwritten(t *testing.T) {
	home := isolateHome(t)
	path := filepath.Join(home, ".claude", "settings.json")
	for _, content := range []string{`{"hooks":`, `{"hooks":"wrong"}`, `{"hooks":{"Stop":[{"hooks":"wrong"}]}}`, `[]`, `{} {}`} {
		writeConfig(t, path, content)
		if _, err := Install(); err == nil {
			t.Fatalf("Install accepted %s", content)
		}
		if got := string(readConfig(t, path)); got != content {
			t.Fatalf("file changed from %q to %q", content, got)
		}
	}
}

func TestInstallWritesThroughASymbolicLink(t *testing.T) {
	home := isolateHome(t)
	dotfiles := filepath.Join(home, "dotfiles", "settings.json")
	writeConfig(t, dotfiles, `{"theme": "dark"}`)
	link := filepath.Join(home, ".claude", "settings.json")
	if err := os.Symlink(dotfiles, link); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}

	if _, err := Install(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("install replaced the symbolic link with a file")
	}
	if !bytes.Contains(readConfig(t, dotfiles), []byte("slopcop hook claude")) {
		t.Fatal("install did not write the linked file")
	}
}

func TestInstallUsesClaudeConfigDirAndPreservesMode(t *testing.T) {
	home := isolateHome(t)
	custom := filepath.Join(home, "custom-claude")
	t.Setenv("CLAUDE_CONFIG_DIR", custom)
	if err := os.MkdirAll(custom, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(custom, "settings.json")
	writeConfig(t, path, `{"custom":true}`)
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}

	if _, err := Install(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(readConfig(t, path), []byte("slopcop hook claude")) {
		t.Fatal("install did not use CLAUDE_CONFIG_DIR")
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("install wrote ~/.claude/settings.json: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows reports only a read-only bit, so 0o640 reads back as 0o666.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}

func TestInstallAddsTheCodexHook(t *testing.T) {
	home := isolateHome(t)
	codex := filepath.Join(home, "custom-codex")
	t.Setenv("CODEX_HOME", codex)
	if err := os.MkdirAll(codex, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(codex, "hooks.json")
	writeConfig(t, path, `{"description": "keep", "hooks": {"Stop": [{"matcher": "mine", "hooks": [{"type": "command", "command": "mine"}]}]}}`)

	result, err := Install()
	if err != nil {
		t.Fatal(err)
	}
	if !result.NeedsTrust || len(result.Installed) != 2 || len(result.ChangedPaths) != 2 {
		t.Fatalf("result = %+v", result)
	}
	data := readConfig(t, path)
	for _, want := range []string{
		`"description": "keep"`, `"command": "mine"`,
		`"command": "slopcop hook codex || true"`, `"commandWindows": "slopcop hook codex || exit /b 0"`, `"timeout": 720`,
	} {
		if !bytes.Contains(data, []byte(want)) {
			t.Errorf("hooks.json lacks %s:\n%s", want, data)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".codex")); !os.IsNotExist(err) {
		t.Fatalf("install used ~/.codex instead of CODEX_HOME: %v", err)
	}

	// An unchanged hook keeps its hash, so Codex keeps trusting it.
	result, err = Install()
	if err != nil || result.NeedsTrust || len(result.ChangedPaths) != 0 {
		t.Fatalf("second install = %+v, %v", result, err)
	}
	if _, err := Remove(); err != nil {
		t.Fatal(err)
	}
	removed := readConfig(t, path)
	if bytes.Contains(removed, []byte("slopcop hook")) || !bytes.Contains(removed, []byte(`"command": "mine"`)) {
		t.Fatalf("hooks.json after removal:\n%s", removed)
	}
}

func TestAnUnreadableSettingsFileChangesNoOther(t *testing.T) {
	home := isolateHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, filepath.Join(home, ".codex", "hooks.json"), `{"hooks":`)
	if _, err := Install(); err == nil {
		t.Fatal("Install accepted a malformed hooks.json")
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("Install wrote Claude Code's settings anyway: %v", err)
	}
}

func TestNoHarnessInstallsNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	result, err := Install()
	if err != nil || len(result.ChangedPaths) != 0 || len(result.Installed) != 0 {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
		t.Fatalf("home entries = %v, err = %v", entries, err)
	}
}

func TestCommandNeverExitsTwo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake slopcop is a shell script")
	}
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	// A slopcop from before the hook exits 2 on it.
	script := "#!/bin/sh\necho 'unknown command \"hook\"' >&2\nexit 2\n"
	if err := os.WriteFile(filepath.Join(bin, "slopcop"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, h := range []harness{{name: "claude"}, {name: "codex"}} {
		if out, err := exec.Command("sh", "-c", h.command()).CombinedOutput(); err != nil {
			t.Fatalf("%q failed: %v\n%s", h.command(), err, out)
		}
	}
}

func TestCommandProblemProbesTheSlopcopOnPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake slopcop is a shell script")
	}
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	if problem := CommandProblem(); !strings.Contains(problem, "no slopcop is on PATH") {
		t.Fatalf("problem with none on PATH = %q", problem)
	}
	fake := filepath.Join(bin, "slopcop")
	for _, tc := range []struct {
		script string
		ok     bool
	}{
		{"#!/bin/sh\necho '{}'\n", true},
		{"#!/bin/sh\necho 'unknown command \"hook\"' >&2\nexit 2\n", false},
	} {
		if err := os.WriteFile(fake, []byte(tc.script), 0o755); err != nil {
			t.Fatal(err)
		}
		if problem := CommandProblem(); (problem == "") != tc.ok {
			t.Fatalf("script %q: problem = %q", tc.script, problem)
		}
	}
}
