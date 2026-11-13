// Package hooks installs and removes SlopCop's user-level Stop hooks and push
// gates for Claude Code, Codex and Grok.
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	fileMode = 0o600
	// CheckTimeout bounds the hook's wait for a check's result. It stays
	// below hookTimeoutSeconds, with room for the local work before the
	// check is sent, so the hook answers before the harness stops waiting.
	CheckTimeout = 10 * time.Minute
	// hookTimeoutSeconds and statusMessage are part of the handler, which
	// Codex trusts by its hash: changing either makes every Codex user
	// trust the hook again, so they are fixed, not derived.
	hookTimeoutSeconds = "720"
	statusMessage      = "Checking the change with SlopCop"
)

// Result describes what an install or removal did.
type Result struct {
	// ChangedPaths are the settings files it changed.
	ChangedPaths []string
	// Installed names the harnesses whose settings hold the hook after an
	// install, whether or not this install changed them.
	Installed []string
	// Gates names the harnesses whose settings hold the push gate after an
	// install.
	Gates []string
	// NeedsTrust says the hook is installed in a harness that runs it only
	// once someone trusts it, as Codex does with /hooks.
	NeedsTrust bool
}

// harness is an agent harness the hook installs into.
type harness struct {
	// name is the harness `slopcop hook` takes.
	name string
	// dir is the harness's configuration directory. The harness counts as
	// installed here when it exists.
	dir string
	// file is the settings file in dir that holds its hooks.
	file string
	// windowsCommand, when set, is the command on Windows, for a harness
	// that runs hooks through cmd /C, which has no true.
	windowsCommand string
	// needsTrust says the harness runs a new or changed hook only once
	// someone trusts it.
	needsTrust bool
	// event selects Stop by default or PreToolUse for the push gate.
	event string
	// gate uses an unguarded command so the harness can block a push with
	// exit 2.
	gate bool
	// matcher is the tool-name matcher of the push gate's group.
	matcher string
}

// Push gate matchers. Claude Code reads a matcher with characters other than
// letters, digits, _ and | as an unanchored regex, so the anchors keep it from
// matching tools such as BashOutput; the MCP tools are GitHub's, under any
// server name, that open a pull request or push files. Grok reads the same
// .claude/settings.local.json and also tests a regex against Claude's names
// for its tools, so ^Bash$ matches its run_terminal_command. Codex sends
// every shell command as Bash and matches a plain name exactly.
const (
	claudeGateMatcher = "^(Bash|mcp__.+__(create_pull_request|push_files))$"
	codexGateMatcher  = "Bash"
	grokGateMatcher   = "^(run_terminal_command|Bash|mcp__.+__(create_pull_request|push_files))$"
)

// harnesses are the harnesses user-level hooks install into, each at the
// configuration directory it reads: CLAUDE_CONFIG_DIR or ~/.claude for
// Claude Code, as the attribution package reads it, CODEX_HOME or
// ~/.codex for Codex, and GROK_HOME or ~/.grok for Grok. A harness whose
// directory cannot be told is left out.
func harnesses() []harness {
	home, homeErr := os.UserHomeDir()
	dir := func(variable, fallback string) string {
		if value := strings.TrimSpace(os.Getenv(variable)); value != "" {
			return value
		}
		if homeErr != nil {
			return ""
		}
		return filepath.Join(home, fallback)
	}
	all := []harness{
		{name: "claude", dir: dir("CLAUDE_CONFIG_DIR", ".claude"), file: "settings.json"},
		{
			name: "codex", dir: dir("CODEX_HOME", ".codex"), file: "hooks.json",
			windowsCommand: "slopcop hook codex || exit /b 0", needsTrust: true,
		},
		// Grok merges every *.json in ~/.grok/hooks, so SlopCop keeps a
		// file of its own there. Grok asks to trust project hooks, not
		// these.
		{name: "grok", dir: dir("GROK_HOME", ".grok"), file: filepath.Join("hooks", "slopcop.json")},
	}
	var known []harness
	for _, h := range all {
		if h.dir != "" {
			known = append(known, h)
		}
	}
	return known
}

// userHarnesses are the Stop hook and push gate entries user-level init
// installs. Claude Code and Codex keep both in their normal settings files.
// Grok loads JSON hook files of the same shape from its hooks directory.
func userHarnesses() []harness {
	var all []harness
	for _, h := range harnesses() {
		all = append(all, h)
		gate := h
		gate.event = "PreToolUse"
		gate.gate = true
		switch h.name {
		case "claude":
			gate.matcher = claudeGateMatcher
		case "codex":
			gate.matcher = codexGateMatcher
		case "grok":
			gate.matcher = grokGateMatcher
		}
		all = append(all, gate)
	}
	return all
}

// command runs the hook. A harness takes exit 2 from a Stop hook as an order
// to keep the agent going, and a slopcop that cannot run the hook, such as
// one from before it, exits 2; || true turns any failure into one that lets
// the agent stop.
func (h harness) command() string {
	if h.gate {
		return "slopcop gate --hook " + h.name
	}
	return "slopcop hook " + h.name + " || true"
}

// ours reports whether a handler's command is SlopCop's hook for this
// harness, as installed now or as first installed, without the guard.
func (h harness) ours(command any) bool {
	if h.gate {
		return command == h.command()
	}
	return command == h.command() || command == "slopcop hook "+h.name
}

// handler is SlopCop's hook handler for this harness. It stays the same from
// one release to the next, since Codex trusts a hook by its hash.
func (h harness) handler() *object {
	handler := newObject()
	handler.set("type", "command")
	handler.set("command", h.command())
	if h.gate {
		return handler
	}
	if h.windowsCommand != "" {
		handler.set("commandWindows", h.windowsCommand)
	}
	handler.set("timeout", json.Number(hookTimeoutSeconds))
	handler.set("statusMessage", statusMessage)
	return handler
}

// repositoryHarnesses are the hooks Repository installs in a clone.
func repositoryHarnesses(root string) []harness {
	claude, codex := filepath.Join(root, ".claude"), filepath.Join(root, ".codex")
	return []harness{
		{name: "claude", dir: claude, file: "settings.local.json"},
		{name: "claude", dir: claude, file: "settings.local.json", event: "PreToolUse", gate: true, matcher: claudeGateMatcher},
		{name: "codex", dir: codex, file: "hooks.json", windowsCommand: "slopcop hook codex || exit /b 0", needsTrust: true},
		{name: "codex", dir: codex, file: "hooks.json", event: "PreToolUse", gate: true, matcher: codexGateMatcher, needsTrust: true},
	}
}

// Repository installs or removes hooks in clone-local settings. skip leaves a
// tracked settings file untouched, preserving the repository's own content.
// Codex 0.166 reads .codex/hooks.json from a project it trusts, as a
// "project" hook that still needs /hooks trust (checked with its app-server
// hooks/list). Codex 0.167 runs a PreToolUse hook before its shell tool with
// tool_name Bash and tool_input.command, and blocks the command when the hook
// exits 2 with a reason on stderr (codex-rs/hooks/src/events/pre_tool_use.rs),
// as Claude Code does, so both get the push gate.
func Repository(root string, install bool, skip func(string) bool) (Result, error) {
	var result Result
	var errs []error
	for _, h := range repositoryHarnesses(root) {
		path := filepath.Join(h.dir, h.file)
		if skip != nil && skip(path) {
			continue
		}
		if install {
			if err := os.MkdirAll(h.dir, 0755); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		changed, present, err := reconcileHarnessGroup([]harness{h}, install)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if changed != "" {
			result.ChangedPaths = append(result.ChangedPaths, changed)
		}
		if install && present {
			if h.gate {
				result.Gates = append(result.Gates, h.name)
			} else {
				result.Installed = append(result.Installed, h.name)
			}
			result.NeedsTrust = result.NeedsTrust || h.needsTrust
		}
	}
	return result, errors.Join(errs...)
}

// GateInstalled reports whether user-level Claude Code, Codex or Grok settings
// hold SlopCop's push gate. A check says whether its tree may be pushed only
// where the gate will ask; elsewhere, such as in CI, the line would be noise.
// The root is accepted for compatibility; installation is machine-wide now.
func GateInstalled(_ string) bool {
	for _, h := range userHarnesses() {
		if !h.gate {
			continue
		}
		data, err := os.ReadFile(filepath.Join(h.dir, h.file))
		if err == nil && bytes.Contains(data, []byte(h.command())) {
			return true
		}
	}
	return false
}

// Install registers SlopCop's Stop hooks and push gates in each harness
// installed here.
func Install() (Result, error) {
	return reconcile(true)
}

// Remove unregisters SlopCop's Stop hooks and push gates from each harness.
func Remove() (Result, error) {
	return reconcile(false)
}

// reconcile updates each harness on its own: a settings file it cannot read
// or write is left as it was and named in the error, and the other harnesses
// are updated all the same. Result says what changed either way.
func reconcile(install bool) (Result, error) {
	var result Result
	found := userHarnesses()
	if len(found) == 0 {
		return result, errors.New("locate the user home directory: set HOME, or CLAUDE_CONFIG_DIR and CODEX_HOME")
	}
	var groups [][]harness
	byPath := map[string]int{}
	for _, h := range found {
		path := filepath.Join(h.dir, h.file)
		index, ok := byPath[path]
		if !ok {
			index = len(groups)
			byPath[path] = index
			groups = append(groups, nil)
		}
		groups[index] = append(groups[index], h)
	}
	var errs []error
	for _, group := range groups {
		changed, present, err := reconcileHarnessGroup(group, install)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if changed != "" {
			result.ChangedPaths = appendUnique(result.ChangedPaths, changed)
		}
		if install && present {
			for _, h := range group {
				result.Installed = appendUnique(result.Installed, h.name)
				if h.gate {
					result.Gates = appendUnique(result.Gates, h.name)
				}
				result.NeedsTrust = result.NeedsTrust || h.needsTrust
			}
		}
	}
	return result, errors.Join(errs...)
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

// reconcileHarness adds or removes the hook in one harness's settings. It
// returns the settings file when it changed it, and whether the harness is
// installed here.
func reconcileHarnessGroup(group []harness, install bool) (string, bool, error) {
	h := group[0]
	present, err := harnessDirExists(h.dir)
	if err != nil || !present {
		return "", false, err
	}
	path := filepath.Join(h.dir, h.file)
	// A settings file linked from elsewhere, as dotfiles often are, is
	// written where the link points, so the link survives.
	target, err := resolveLink(path)
	if err != nil {
		return "", true, err
	}
	data, mode, exists, err := readExisting(target)
	if err != nil {
		return "", true, err
	}
	if !exists && !install {
		return "", true, nil
	}
	updated := data
	changedAny := false
	for _, event := range group {
		var changed bool
		updated, changed, err = reconcileJSON(updated, install, event)
		if err != nil {
			return "", true, fmt.Errorf("reconcile %s: %w", path, err)
		}
		changedAny = changedAny || changed
	}
	if !changedAny {
		return "", true, nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return "", true, fmt.Errorf("create %s: %w", filepath.Dir(target), err)
	}
	if err := writeAtomic(target, updated, mode); err != nil {
		return "", true, err
	}
	return path, true, nil
}

// CommandProblem says why the slopcop a harness finds on PATH could not run
// the hook of each of harnesses, or nothing when it can. The command's guard
// keeps a failure from holding the agent, but the failure would leave every
// check undone without a word. A slopcop that knows a harness reads the
// payload and, given none, says so; one that does not answers {} or fails.
func CommandProblem(harnesses []string) string {
	found, err := exec.LookPath("slopcop")
	if err != nil {
		return "no slopcop is on PATH, so no harness can run the hook"
	}
	for _, name := range harnesses {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		out, err := exec.CommandContext(ctx, found, "hook", name).Output()
		cancel()
		var answer struct {
			SystemMessage string `json:"systemMessage"`
		}
		if err != nil || json.Unmarshal(out, &answer) != nil || answer.SystemMessage == "" {
			return fmt.Sprintf("%s, the slopcop first on PATH, cannot run the %s hook, so no automatic check will run there", found, name)
		}
	}
	return ""
}

func harnessDirExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	return info.IsDir(), nil
}

// resolveLink follows symbolic links at path to the file they name. A path
// that does not exist yet is its own target; a link to nowhere is refused
// rather than replaced.
func resolveLink(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("resolve %s: %w", path, err)
	}
	if _, lstatErr := os.Lstat(path); lstatErr == nil {
		return "", fmt.Errorf("%s is a symbolic link to a file that does not exist", path)
	}
	return path, nil
}

func readExisting(path string) ([]byte, fs.FileMode, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fileMode, false, nil
	}
	if err != nil {
		return nil, 0, false, fmt.Errorf("read %s: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, false, fmt.Errorf("stat %s: %w", path, err)
	}
	return data, info.Mode().Perm(), true, nil
}

func writeAtomic(path string, data []byte, mode fs.FileMode) (err error) {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create a temporary file for %s: %w", path, err)
	}
	tempPath := temp.Name()
	defer func() {
		if temp != nil {
			_ = temp.Close()
		}
		if err != nil {
			_ = os.Remove(tempPath)
		}
	}()
	if err = temp.Chmod(mode); err != nil {
		return fmt.Errorf("set permissions on a temporary file for %s: %w", path, err)
	}
	if _, err = temp.Write(data); err != nil {
		return fmt.Errorf("write a temporary file for %s: %w", path, err)
	}
	if err = temp.Sync(); err != nil {
		return fmt.Errorf("sync a temporary file for %s: %w", path, err)
	}
	if err = temp.Close(); err != nil {
		return fmt.Errorf("close a temporary file for %s: %w", path, err)
	}
	temp = nil
	if err = os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// Presence says which of SlopCop's user-level hooks one harness has.
type Presence struct {
	// Configured says the harness's configuration directory exists, so the
	// harness is installed here.
	Configured bool
	// Stop is the Stop hook.
	Stop bool
	// Gate is the push gate.
	Gate bool
	// NeedsTrust says the harness runs these hooks only once someone
	// trusts them, which cannot be read from its files.
	NeedsTrust bool
}

// Find reports, for each harness init installs into, which of SlopCop's
// user-level hooks its settings hold. It only reads files.
func Find() map[string]Presence {
	found := map[string]Presence{}
	for _, h := range userHarnesses() {
		p := found[h.name]
		p.Configured, _ = harnessDirExists(h.dir)
		p.NeedsTrust = p.NeedsTrust || h.needsTrust
		if fileHolds(filepath.Join(h.dir, h.file), h, true) {
			if h.gate {
				p.Gate = true
			} else {
				p.Stop = true
			}
		}
		found[h.name] = p
	}
	return found
}

// LegacyRepositoryHooks lists the clone-local settings files under root that
// still hold a hook written by the releases that installed per repository,
// leaving out those skip accepts, as the cleanup does for tracked files. A
// handler counts under any event or matcher, since the cleanup removes it
// wherever it is.
func LegacyRepositoryHooks(root string, skip func(string) bool) []string {
	var paths []string
	for _, h := range repositoryHarnesses(root) {
		path := filepath.Join(h.dir, h.file)
		if skip != nil && skip(path) {
			continue
		}
		if fileHolds(path, h, false) {
			paths = appendUnique(paths, path)
		}
	}
	return paths
}

// fileHolds reports whether the settings file at path has h's handler. With
// strict set, the handler counts only where the harness would run it for h:
// under h's event and, for the push gate, in a group with h's matcher, as
// install writes it. Otherwise it counts under any event. An unreadable or
// malformed file holds none.
func fileHolds(path string, h harness, strict bool) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	root, err := decodeSettings(data)
	if err != nil {
		return false
	}
	hooks, err := childObject(root, "hooks", false)
	if err != nil || hooks == nil {
		return false
	}
	event := h.event
	if event == "" {
		event = "Stop"
	}
	for _, key := range hooks.keys {
		if strict && key != event {
			continue
		}
		groups, _ := hooks.get(key)
		list, _ := groups.([]any)
		for _, groupValue := range list {
			group, ok := groupValue.(*object)
			if !ok || strict && h.gate && groupMatcher(group) != h.matcher {
				continue
			}
			handlersValue, _ := group.get("hooks")
			handlers, _ := handlersValue.([]any)
			for _, handlerValue := range handlers {
				handler, ok := handlerValue.(*object)
				if !ok {
					continue
				}
				if command, _ := handler.get("command"); h.ours(command) {
					return true
				}
			}
		}
	}
	return false
}
