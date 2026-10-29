// Package hooks installs and removes SlopCop's Claude Code Stop hook.
package hooks

import (
	"context"
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
	command  = "slopcop hook claude"
	fileMode = 0o600
	// CheckTimeout bounds the hook's wait for a check's result.
	CheckTimeout = 10 * time.Minute
	// hookTimeout is the timeout Claude Code gives the hook. It leaves room
	// beyond CheckTimeout for the local work before the check is sent, so
	// the hook answers before Claude Code stops waiting for it.
	hookTimeout = CheckTimeout + 2*time.Minute
)

// Result describes the files an install or removal changed.
type Result struct {
	ChangedPaths []string
	// Installed says the hook is in Claude Code's settings after an
	// install, whether or not this install changed them.
	Installed bool
}

// Install registers SlopCop's Stop hook when Claude Code is installed here.
func Install() (Result, error) {
	return reconcile(true)
}

// Remove unregisters SlopCop's Stop hook.
func Remove() (Result, error) {
	return reconcile(false)
}

func reconcile(install bool) (Result, error) {
	var result Result
	dir, err := claudeConfigDir()
	if err != nil {
		return result, err
	}
	found, err := harnessDirExists(dir)
	if err != nil || !found {
		return result, err
	}
	path := filepath.Join(dir, "settings.json")
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
		return result, nil
	}
	updated, changed, err := reconcileJSON(data, install)
	if err != nil {
		return result, fmt.Errorf("reconcile %s: %w", path, err)
	}
	result.Installed = install
	if !changed {
		return result, nil
	}
	if err := writeAtomic(target, updated, mode); err != nil {
		return Result{}, err
	}
	result.ChangedPaths = append(result.ChangedPaths, path)
	return result, nil
}

// CommandProblem says why the slopcop Claude Code finds on PATH could not
// run the hook, or nothing when it can. A slopcop from before the hook exits
// 2 on it, which Claude Code takes from a Stop hook as an order to keep the
// agent going, so the hook is asked to answer as Claude Code would ask it.
func CommandProblem() string {
	found, err := exec.LookPath("slopcop")
	if err != nil {
		return "no slopcop is on PATH, so Claude Code cannot run the hook"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Without "claude" the hook reads nothing and answers {}.
	out, err := exec.CommandContext(ctx, found, "hook").Output()
	if err != nil || strings.TrimSpace(string(out)) != "{}" {
		return fmt.Sprintf("%s, the slopcop first on PATH, cannot run the hook, and each time it fails Claude Code would keep the agent going", found)
	}
	return ""
}

// claudeConfigDir is where Claude Code reads its settings: CLAUDE_CONFIG_DIR
// when it is set, as the attribution package reads it, and ~/.claude
// otherwise.
func claudeConfigDir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate the user home directory: %w", err)
	}
	return filepath.Join(home, ".claude"), nil
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
