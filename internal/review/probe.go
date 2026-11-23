package review

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ProbeResult records whether a reviewer can complete a short prompt.
type ProbeResult struct {
	Family  Family
	Adapter string
	Model   string
	Effort  string
	// Warning says how the reviewer would run differently from what its
	// family asks for, such as on Copilot's Auto model.
	Warning   string
	Available bool
	Reason    string
	// Skipped is set when the probe never started the adapter, because
	// what the family needs is plainly missing; Reason then says what.
	Skipped bool
}

// probeTimeout bounds one reviewer's probe, from adapter launch to its reply.
// The first run of an adapter includes its npx download.
var probeTimeout = 3 * time.Minute

// Probe checks families without changing the configured review list.
//
// Families that share an adapter command are probed one after another, so a
// first-run npx install of one package never overlaps itself; different
// adapters are probed at the same time. done, when not nil, is called once per
// family as soon as its result is known, never concurrently.
func Probe(ctx context.Context, root, logDir string, families []Family, settings Settings, done func(ProbeResult)) []ProbeResult {
	results := make([]ProbeResult, len(families))
	var mu sync.Mutex
	report := func(i int, r ProbeResult) {
		mu.Lock()
		defer mu.Unlock()
		results[i] = r
		if done != nil {
			done(r)
		}
	}
	if err := os.MkdirAll(logDir, 0700); err != nil {
		for i, f := range families {
			report(i, ProbeResult{Family: f, Reason: err.Error()})
		}
		return results
	}
	var groups [][]int
	byCommand := map[string]int{}
	commands := make([][]string, len(families))
	adapters := make([]string, len(families))
	var copilot *copilotCatalog
	for i, f := range families {
		if reason := MissingPrerequisite(f, root, settings); reason != "" {
			report(i, ProbeResult{Family: f, Reason: reason, Skipped: true})
			continue
		}
		argv := CommandFor(f, settings)
		adapters[i] = AdapterFor(f, settings)
		if adapters[i] == copilotAdapter && copilot == nil {
			copilot = newCopilotCatalog(argv)
		}
		commands[i] = argv
		key := strings.Join(argv, "\x00")
		g, ok := byCommand[key]
		if !ok {
			g = len(groups)
			byCommand[key] = g
			groups = append(groups, nil)
		}
		groups[g] = append(groups[g], i)
	}
	var wg sync.WaitGroup
	for _, group := range groups {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, i := range group {
				report(i, probeOne(ctx, root, logDir, adapters[i], commands[i], copilot, families[i], fmt.Sprintf("%d-%s", i+1, families[i])))
			}
		}()
	}
	wg.Wait()
	return results
}

// probeOne starts one adapter, prepares a restricted session and sends it a
// one-word prompt.
func probeOne(ctx context.Context, root, logDir, adapter string, argv []string, copilot *copilotCatalog, f Family, name string) ProbeResult {
	r := ProbeResult{Family: f, Adapter: adapter}
	s, err := launch(ctx, root, reviewTrees{}, adapter, argv, copilot, f, name, logDir, probeTimeout)
	if err != nil {
		r.Reason = err.Error()
		return r
	}
	r.Model, r.Effort, r.Warning = s.result.Model, s.result.Effort, s.result.Warning
	reply, err := s.prompt("Reply with exactly the word ok and nothing else. Do not use any tools.")
	switch {
	case err != nil:
		r.Reason = err.Error()
	case reply.Text == "":
		r.Reason = "empty reply"
	default:
		r.Available = true
	}
	if !r.Available && r.Model == copilotAuto {
		copilot.release(f)
	}
	return r
}

// MissingPrerequisite names what a family's default adapter, run in root,
// needs and this machine plainly lacks: an API key, or any sign that its harness was ever
// logged in. The probe skips such a family instead of downloading an adapter
// that cannot work. A configured adapter command may carry its own
// credentials, so it never lacks anything here. The checks look only for
// what is certainly absent; a harness that keeps its login somewhere they
// do not look is still probed.
func MissingPrerequisite(f Family, root string, settings Settings) string {
	adapter := AdapterFor(f, settings)
	if len(settings.Adapters[adapter]) > 0 {
		return ""
	}
	switch adapter {
	case copilotAdapter:
		if !CopilotMayBeLoggedIn() {
			return "GitHub Copilot CLI is not logged in"
		}
	case "dsh":
		if os.Getenv("DEEPSEEK_API_KEY") == "" {
			return "DEEPSEEK_API_KEY is not set"
		}
	case grokAdapter:
		if strings.TrimSpace(os.Getenv("XAI_API_KEY")) == "" {
			return "XAI_API_KEY is not set"
		}
	case kimiAdapter:
		if strings.TrimSpace(os.Getenv(kimiKeyEnv)) == "" {
			return kimiKeyEnv + " is not set"
		}
	case "codex":
		if !codexMayBeLoggedIn() {
			return "Codex is not logged in"
		}
	case "claude":
		if !claudeMayBeLoggedIn(root) {
			return "Claude Code is not logged in"
		}
	}
	return ""
}

// codexMayBeLoggedIn reports whether Codex has an API key or the adapter's
// automatic login request in the environment, a saved login, or a config
// that may keep the login in the system keyring or name another provider.
func codexMayBeLoggedIn() bool {
	if anyEnv("OPENAI_API_KEY", "CODEX_API_KEY", "DEFAULT_AUTH_REQUEST") {
		return true
	}
	home := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if home == "" {
		user, err := os.UserHomeDir()
		if err != nil {
			return true
		}
		home = filepath.Join(user, ".codex")
	}
	if exists(filepath.Join(home, "auth.json")) {
		return true
	}
	// The login may sit in the keyring, or a custom provider may bring
	// its own key.
	config, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		return false
	}
	for _, key := range []string{"cli_auth_credentials_store", "model_provider"} {
		if strings.Contains(string(config), key) {
			return true
		}
	}
	return false
}

// claudeEnv names the environment variables that give Claude Code a key or
// another provider, in the process environment or a settings file's env.
var claudeEnv = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"}

// ClaudeManagedSettings are the system-wide settings files, and their
// drop-in directories, that Claude Code reads on each platform. Tests blank
// it to see a fresh machine.
var ClaudeManagedSettings = []string{
	"/Library/Application Support/ClaudeCode/managed-settings.json",
	"/Library/Application Support/ClaudeCode/managed-settings.d",
	"/etc/claude-code/managed-settings.json",
	"/etc/claude-code/managed-settings.d",
	`C:\Program Files\ClaudeCode\managed-settings.json`,
	`C:\Program Files\ClaudeCode\managed-settings.d`,
	`C:\ProgramData\ClaudeCode\managed-settings.json`,
	`C:\ProgramData\ClaudeCode\managed-settings.d`,
}

// claudeMayBeLoggedIn reports whether Claude Code has a key or provider in
// the environment or its settings, or has ever run here: it writes
// .claude.json on first run, and keeps the login there, in
// .credentials.json, or in the macOS keychain. A key may also sit in the
// user's or root's project settings. Init itself writes settings.json for its
// hooks, so only a key in it counts, not the file.
func claudeMayBeLoggedIn(root string) bool {
	if anyEnv(claudeEnv...) {
		return true
	}
	user, err := os.UserHomeDir()
	if err != nil {
		return true
	}
	dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR"))
	if dir == "" {
		dir = filepath.Join(user, ".claude")
	}
	// An administrator's managed settings may hold a key or provider; any
	// such file is reason enough to probe.
	paths := append([]string{filepath.Join(dir, ".claude.json"), filepath.Join(dir, ".credentials.json"), filepath.Join(user, ".claude.json")}, ClaudeManagedSettings...)
	for _, p := range paths {
		if exists(p) {
			return true
		}
	}
	var settingsFiles []string
	for _, d := range []string{dir, filepath.Join(root, ".claude")} {
		settingsFiles = append(settingsFiles, filepath.Join(d, "settings.json"), filepath.Join(d, "settings.local.json"))
	}
	for _, path := range settingsFiles {
		settings, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, key := range append([]string{"apiKeyHelper"}, claudeEnv...) {
			if strings.Contains(string(settings), `"`+key+`"`) {
				return true
			}
		}
	}
	return false
}

func anyEnv(names ...string) bool {
	for _, n := range names {
		if strings.TrimSpace(os.Getenv(n)) != "" {
			return true
		}
	}
	return false
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
