// Package config stores the service URL and token the CLI uses between runs.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const fileMode = 0o600

// DefaultServiceURL is the hosted service the CLI talks to when no flag,
// environment variable, or stored value names another one.
const DefaultServiceURL = "https://slopcop.com"

// Config is the per-user slopcop configuration. It lives outside any
// repository because it contains a credential.
type Config struct {
	ServiceURL string `json:"serviceUrl,omitempty"`
	Token      string `json:"token,omitempty"`
}

// Path returns the configuration file location.
func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate the user config directory: %w", err)
	}
	return filepath.Join(dir, "slopcop", "config.json"), nil
}

// NoticePath returns the file, next to the configuration, that records when
// the last upgrade notice was printed.
func NoticePath() (string, error) {
	path, err := Path()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), "update-notice"), nil
}

// Load reads the configuration. A missing file is not an error.
func Load() (Config, error) {
	var cfg Config
	path, err := Path()
	if err != nil {
		return cfg, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// Save writes the configuration owner-only and returns the path.
func Save(cfg Config) (string, error) {
	path, err := Path()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, fileMode); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Chmod(path, fileMode); err != nil {
		return "", fmt.Errorf("secure %s: %w", path, err)
	}
	return path, nil
}

// ClearToken forgets the stored token and keeps the service URL. The boolean
// reports whether a token was actually stored.
func ClearToken() (bool, error) {
	cfg, err := Load()
	if err != nil {
		return false, err
	}
	if cfg.Token == "" {
		return false, nil
	}
	cfg.Token = ""
	if _, err := Save(cfg); err != nil {
		return false, err
	}
	return true, nil
}
