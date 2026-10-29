// Package hooks installs and removes SlopCop's Stop hook for Claude Code and
// Codex.
package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	fileMode = 0o600
	// CheckTimeout bounds the hook's wait for a check's result.
	CheckTimeout = 10 * time.Minute
	// hookTimeout is the timeout a harness gives the hook. It leaves room
	// beyond CheckTimeout for the local work before the check is sent, so
	// the hook answers before the harness stops waiting for it.
	hookTimeout = CheckTimeout + 2*time.Minute
)

// Result describes what an install or removal did.
type Result struct {
	// ChangedPaths are the settings files it changed.
	ChangedPaths []string
	// Installed names the harnesses whose settings hold the hook after an
	// install, whether or not this install changed them.
	Installed []string
	// NeedsTrust says the install changed Codex's hook, which Codex runs
	// only once someone trusts it with /hooks.
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
}

// harnesses are the harnesses the hook installs into, each at the
// configuration directory it reads: CLAUDE_CONFIG_DIR or ~/.claude for
// Claude Code, as the attribution package reads it, and CODEX_HOME or
// ~/.codex for Codex.
func harnesses() ([]harness, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("locate the user home directory: %w", err)
	}
	dir := func(variable, fallback string) string {
		if value := strings.TrimSpace(os.Getenv(variable)); value != "" {
			return value
		}
		return filepath.Join(home, fallback)
	}
	return []harness{
		{name: "claude", dir: dir("CLAUDE_CONFIG_DIR", ".claude"), file: "settings.json"},
		{name: "codex", dir: dir("CODEX_HOME", ".codex"), file: "hooks.json"},
	}, nil
}

// command runs the hook. A harness takes exit 2 from a Stop hook as an order
// to keep the agent going, and a slopcop that cannot run the hook, such as
// one from before it, exits 2; || true turns any failure into one that lets
// the agent stop.
func (h harness) command() string {
	return "slopcop hook " + h.name + " || true"
}

// ours reports whether a handler's command is SlopCop's hook for this
// harness, as installed now or as first installed, without the guard.
func (h harness) ours(command any) bool {
	return command == h.command() || command == "slopcop hook "+h.name
}

// handler is SlopCop's hook handler for this harness. Codex trusts a hook by
// the hash of its handler, so the handler stays the same from one release
// to the next.
func (h harness) handler() *object {
	handler := newObject()
	handler.set("type", "command")
	handler.set("command", h.command())
	if h.name == "codex" {
		// Codex runs a hook through cmd /C on Windows, which has no true.
		handler.set("commandWindows", "slopcop hook codex || exit /b 0")
	}
	handler.set("timeout", json.Number(strconv.Itoa(int(hookTimeout/time.Second))))
	handler.set("statusMessage", statusMessage)
	return handler
}

// Install registers SlopCop's Stop hook in each harness installed here.
func Install() (Result, error) {
	return reconcile(true)
}

// Remove unregisters SlopCop's Stop hook from each harness.
func Remove() (Result, error) {
	return reconcile(false)
}

// update is a settings file to rewrite.
type update struct {
	harness harness
	path    string
	target  string
	data    []byte
	mode    fs.FileMode
}

// reconcile works out every change before it writes any, so a settings file
// it cannot read leaves the others as they were too.
func reconcile(install bool) (Result, error) {
	var result Result
	found, err := harnesses()
	if err != nil {
		return result, err
	}
	var updates []update
	for _, h := range found {
		present, err := harnessDirExists(h.dir)
		if err != nil {
			return result, err
		}
		if !present {
			continue
		}
		path := filepath.Join(h.dir, h.file)
		// A settings file linked from elsewhere, as dotfiles often are, is
		// written where the link points, so the link survives.
		target, err := resolveLink(path)
		if err != nil {
			return result, err
		}
		data, mode, exists, err := readExisting(target)
		if err != nil {
			return result, err
		}
		if !exists && !install {
			continue
		}
		updated, changed, err := reconcileJSON(data, install, h)
		if err != nil {
			return result, fmt.Errorf("reconcile %s: %w", path, err)
		}
		if install {
			result.Installed = append(result.Installed, h.name)
		}
		if changed {
			updates = append(updates, update{harness: h, path: path, target: target, data: updated, mode: mode})
		}
	}
	for _, u := range updates {
		if err := writeAtomic(u.target, u.data, u.mode); err != nil {
			return result, err
		}
		result.ChangedPaths = append(result.ChangedPaths, u.path)
		if install && u.harness.name == "codex" {
			result.NeedsTrust = true
		}
	}
	return result, nil
}

// CommandProblem says why the slopcop a harness finds on PATH could not run
// the hook, or nothing when it can, by asking it to answer as a harness
// would. The command's guard keeps a failure from holding the agent, but the
// failure would leave every check undone without a word.
func CommandProblem() string {
	found, err := exec.LookPath("slopcop")
	if err != nil {
		return "no slopcop is on PATH, so no harness can run the hook"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Without a harness the hook reads nothing and answers {}.
	out, err := exec.CommandContext(ctx, found, "hook").Output()
	if err != nil || strings.TrimSpace(string(out)) != "{}" {
		return fmt.Sprintf("%s, the slopcop first on PATH, cannot run the hook, so no automatic check will run", found)
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
