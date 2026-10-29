package hooks

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func isolateHomes(t *testing.T) (home, codex string) {
	t.Helper()
	home = t.TempDir()
	codex = filepath.Join(home, ".codex")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CODEX_HOME", codex)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(codex, 0o755); err != nil {
		t.Fatal(err)
	}
	return home, codex
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

func TestInstallIsIdempotentAndRemoveLeavesFeatureSetting(t *testing.T) {
	home, codex := isolateHomes(t)
	claudePath := filepath.Join(home, ".claude", "settings.json")
	codexPath := filepath.Join(codex, "hooks.json")
	configPath := filepath.Join(codex, "config.toml")

	result, err := Install()
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ChangedPaths) != 3 || !result.CodexInstalled {
		t.Fatalf("result = %+v", result)
	}
	for _, path := range []string{claudePath, codexPath} {
		data := readConfig(t, path)
		if bytes.Count(data, []byte(`"command": "slopcop hook`)) != 1 || !bytes.Contains(data, []byte(`"timeout": 660`)) {
			t.Fatalf("%s =\n%s", path, data)
		}
	}
	if got := string(readConfig(t, configPath)); got != "[features]\nhooks = true\n" {
		t.Fatalf("config.toml = %q", got)
	}

	beforeClaude := readConfig(t, claudePath)
	beforeCodex := readConfig(t, codexPath)
	result, err = Install()
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ChangedPaths) != 0 {
		t.Fatalf("second install changed %v", result.ChangedPaths)
	}
	if !bytes.Equal(beforeClaude, readConfig(t, claudePath)) || !bytes.Equal(beforeCodex, readConfig(t, codexPath)) {
		t.Fatal("second install rewrote a settings file")
	}

	result, err = Remove()
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ChangedPaths) != 2 || result.CodexInstalled {
		t.Fatalf("remove result = %+v", result)
	}
	for _, path := range []string{claudePath, codexPath} {
		if bytes.Contains(readConfig(t, path), []byte("slopcop hook")) {
			t.Fatalf("%s still contains SlopCop", path)
		}
	}
	if got := string(readConfig(t, configPath)); got != "[features]\nhooks = true\n" {
		t.Fatalf("remove changed config.toml = %q", got)
	}
	result, err = Remove()
	if err != nil || len(result.ChangedPaths) != 0 {
		t.Fatalf("second remove = %+v, %v", result, err)
	}
}

func TestInstallAndRemovePreserveUserConfiguration(t *testing.T) {
	home, codex := isolateHomes(t)
	claudePath := filepath.Join(home, ".claude", "settings.json")
	writeConfig(t, claudePath, `{
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
	installed := readConfig(t, claudePath)
	if bytes.Count(installed, []byte(`"command": "slopcop hook claude"`)) != 2 {
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
	removed := readConfig(t, claudePath)
	if bytes.Count(removed, []byte(`"command": "slopcop hook claude"`)) != 1 {
		t.Fatalf("remove did not limit itself to Stop:\n%s", removed)
	}
	for _, want := range []string{`"theme": "dark"`, `"command": "my-stop"`, `"PostToolUse"`, `"keep": 12345678901234567890`} {
		if !bytes.Contains(removed, []byte(want)) {
			t.Errorf("removed settings lack %s", want)
		}
	}

	writeConfig(t, filepath.Join(codex, "hooks.json"), `{"description":"keep","hooks":{"Stop":[{"matcher":"mine","hooks":[{"type":"command","command":"mine"}]}]}}`)
	if _, err := Install(); err != nil {
		t.Fatal(err)
	}
	codexSettings := readConfig(t, filepath.Join(codex, "hooks.json"))
	for _, want := range []string{`"description": "keep"`, `"matcher": "mine"`, `"command": "mine"`, `"command": "slopcop hook codex"`} {
		if !bytes.Contains(codexSettings, []byte(want)) {
			t.Errorf("Codex settings lack %s", want)
		}
	}
}

func TestMalformedJSONIsNotOverwritten(t *testing.T) {
	home, _ := isolateHomes(t)
	for _, content := range []string{`{"hooks":`, `{"hooks":"wrong"}`, `{"hooks":{"Stop":[{"hooks":"wrong"}]}}`} {
		path := filepath.Join(home, ".claude", "settings.json")
		writeConfig(t, path, content)
		if _, err := Install(); err == nil {
			t.Fatalf("Install accepted %s", content)
		}
		if got := string(readConfig(t, path)); got != content {
			t.Fatalf("file changed from %q to %q", content, got)
		}
	}
}

func TestInstallUsesCustomCodexHomeAndPreservesMode(t *testing.T) {
	home, _ := isolateHomes(t)
	custom := filepath.Join(home, "custom-codex")
	t.Setenv("CODEX_HOME", custom)
	if err := os.MkdirAll(custom, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(home, ".claude")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(home, ".codex")); err != nil {
		t.Fatal(err)
	}

	if _, err := Install(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(custom, "hooks.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex")); !os.IsNotExist(err) {
		t.Fatalf("default Codex home was created: %v", err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, path, `{"custom":true}`)
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(); err != nil {
		t.Fatal(err)
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

func TestCodexConfigReconciliation(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
		changed           bool
	}{
		{name: "empty", input: "", want: "[features]\nhooks = true\n", changed: true},
		{name: "false", input: "[features]\nhooks = false # keep this comment\n", want: "[features]\nhooks = true # keep this comment\n", changed: true},
		{name: "already true", input: "[features]\nhooks = true\n", changed: false},
		{name: "existing features", input: "[features]\nmodel = \"x\"\n", want: "[features]\nhooks = true\nmodel = \"x\"\n", changed: true},
		{name: "other settings", input: "model = \"x\"\n\n[features]\n\n[ui]\ncolor = true\n", want: "model = \"x\"\n\n[features]\nhooks = true\n\n[ui]\ncolor = true\n", changed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, changed, err := reconcileCodexConfig([]byte(tc.input))
			if err != nil {
				t.Fatal(err)
			}
			if changed != tc.changed || (tc.want != "" && string(got) != tc.want) {
				t.Fatalf("changed = %v, got = %q, want changed %v and %q", changed, got, tc.changed, tc.want)
			}
			_, again, err := reconcileCodexConfig(got)
			if err != nil || again {
				t.Fatalf("second reconciliation changed %q: %v", got, err)
			}
		})
	}
}

func TestAmbiguousCodexConfigIsNotOverwritten(t *testing.T) {
	_, codex := isolateHomes(t)
	if err := os.RemoveAll(filepath.Dir(codex)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(codex, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"[features\nhooks = false\n", "[features]\nhooks = false\nhooks = true\n", "[features]\nhooks = \"yes\"\n"} {
		path := filepath.Join(codex, "config.toml")
		writeConfig(t, path, content)
		if _, err := Install(); err == nil {
			t.Fatalf("Install accepted %q", content)
		}
		if got := string(readConfig(t, path)); got != content {
			t.Fatalf("file changed from %q to %q", content, got)
		}
	}
}

func TestNoHarnessInstallsNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, "missing-codex"))
	result, err := Install()
	if err != nil || len(result.ChangedPaths) != 0 || result.CodexInstalled {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
		t.Fatalf("home entries = %v, err = %v", entries, err)
	}
}

func TestCommandUsesNoShellSpecificSyntax(t *testing.T) {
	for _, command := range []string{claudeCommand, codexCommand} {
		if strings.ContainsAny(command, `"$\\`) {
			t.Fatalf("command %q is not a stable simple command", command)
		}
	}
}
