// Package hooks installs and removes SlopCop's agent harness Stop hooks.
package hooks

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	claudeCommand = "slopcop hook claude"
	codexCommand  = "slopcop hook codex"
	fileMode      = 0o600
)

// Result describes files changed by an install or removal.
type Result struct {
	ChangedPaths   []string
	CodexInstalled bool
}

// Install registers SlopCop's Stop hook for each detected agent harness.
func Install() (Result, error) {
	return reconcile(true)
}

// Remove unregisters SlopCop's Stop hooks without changing Codex feature flags.
func Remove() (Result, error) {
	return reconcile(false)
}

type fileUpdate struct {
	path string
	data []byte
	mode fs.FileMode
}

func reconcile(install bool) (Result, error) {
	var result Result
	home, err := os.UserHomeDir()
	if err != nil {
		return result, fmt.Errorf("locate the user home directory: %w", err)
	}
	claudeDir := filepath.Join(home, ".claude")
	codexDir := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if codexDir == "" {
		codexDir = filepath.Join(home, ".codex")
	}

	claudeFound, err := harnessDirExists(claudeDir)
	if err != nil {
		return result, err
	}
	codexFound, err := harnessDirExists(codexDir)
	if err != nil {
		return result, err
	}

	var updates []fileUpdate
	if claudeFound {
		update, err := prepareJSON(filepath.Join(claudeDir, "settings.json"), claudeCommand, install)
		if err != nil {
			return result, err
		}
		if update != nil {
			updates = append(updates, *update)
		}
	}
	if codexFound {
		update, err := prepareJSON(filepath.Join(codexDir, "hooks.json"), codexCommand, install)
		if err != nil {
			return result, err
		}
		if update != nil {
			updates = append(updates, *update)
		}
		if install {
			update, err = prepareCodexConfig(filepath.Join(codexDir, "config.toml"))
			if err != nil {
				return result, err
			}
			if update != nil {
				updates = append(updates, *update)
			}
		}
	}

	for _, update := range updates {
		if err := writeAtomic(update.path, update.data, update.mode); err != nil {
			return result, err
		}
		result.ChangedPaths = append(result.ChangedPaths, update.path)
	}
	result.CodexInstalled = install && codexFound
	return result, nil
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
