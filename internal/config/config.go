// Package config stores the default service and the login for each service
// the CLI uses between runs.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const fileMode = 0o600

// DefaultServiceURL is the hosted service the CLI talks to when no flag,
// environment variable, or stored value names another one.
const DefaultServiceURL = "https://slopcop.com"

// Config is the per-user slopcop configuration. It lives outside any
// repository because it contains a credential.
type Config struct {
	// ServiceURL is the default service: the one a command talks to when
	// neither --service nor $SLOPCOP_SERVICE names another.
	ServiceURL string `json:"serviceUrl,omitempty"`
	// LegacyToken is the single credential older releases stored, for the
	// service ServiceURL names. Load and Save move it into Logins.
	LegacyToken string `json:"token,omitempty"`
	// Logins holds one credential per service, keyed by Origin, so a
	// credential is only ever sent to the service that issued it.
	Logins map[string]Login `json:"logins,omitempty"`
	Mode   string           `json:"mode,omitempty"`
	Review *Review          `json:"review,omitempty"`
	Gate   *Gate            `json:"gate,omitempty"`
}

// Login is the credential one service issued.
type Login struct {
	Token string `json:"token"`
}

// Origin is the key a service's login is stored under: its scheme and host,
// lowercased, with the port unless it is the scheme's default, and no path.
// An empty URL is the hosted default. A URL that does not parse is its own
// key, so it matches nothing but itself.
func Origin(serviceURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(serviceURL), "/")
	if trimmed == "" {
		return DefaultServiceURL
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" {
		return trimmed
	}
	scheme := strings.ToLower(parsed.Scheme)
	host := strings.ToLower(parsed.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	port := parsed.Port()
	if port != "" && !(scheme == "https" && port == "443") && !(scheme == "http" && port == "80") {
		host += ":" + port
	}
	return scheme + "://" + host
}

// DefaultService is the stored default service, or the hosted one.
func (c Config) DefaultService() string {
	if value := strings.TrimRight(strings.TrimSpace(c.ServiceURL), "/"); value != "" {
		return value
	}
	return DefaultServiceURL
}

// TokenFor is the credential stored for the service, or empty.
func (c Config) TokenFor(serviceURL string) string {
	return strings.TrimSpace(c.Logins[Origin(serviceURL)].Token)
}

// SetToken stores the credential the service issued, replacing only that
// service's earlier one.
func (c *Config) SetToken(serviceURL, token string) {
	if c.Logins == nil {
		c.Logins = map[string]Login{}
	}
	c.Logins[Origin(serviceURL)] = Login{Token: token}
}

// Forget drops the service's credential and reports whether one was stored.
func (c *Config) Forget(serviceURL string) bool {
	origin := Origin(serviceURL)
	if _, stored := c.Logins[origin]; !stored {
		return false
	}
	delete(c.Logins, origin)
	return true
}

// SignedIn lists the origins a credential is stored for, sorted.
func (c Config) SignedIn() []string {
	origins := make([]string, 0, len(c.Logins))
	for origin := range c.Logins {
		origins = append(origins, origin)
	}
	sort.Strings(origins)
	return origins
}

// migrate moves the single credential an older release stored into Logins,
// under the service it was stored with, and keeps that service the default.
// A login already stored for that service is newer and wins.
func (c *Config) migrate() {
	token := strings.TrimSpace(c.LegacyToken)
	c.LegacyToken = ""
	if token == "" {
		return
	}
	if c.TokenFor(c.DefaultService()) == "" {
		c.SetToken(c.DefaultService(), token)
	}
}

// Review contains local reviewer settings.
type Review struct {
	Reviewers []string `json:"reviewers,omitempty"`
	// Fallbacks stand in, in order, for reviewers that fail.
	Fallbacks []string `json:"fallbacks,omitempty"`
	Timeout   string   `json:"timeout,omitempty"`
	// MinCompleted is how many reviewers must finish for a clean review;
	// zero means review.DefaultMinCompleted.
	MinCompleted int                `json:"minCompleted,omitempty"`
	Adapters     map[string]Adapter `json:"adapters,omitempty"`
}

// Gate controls which hosted findings block push approval.
type Gate struct {
	BlockOn string `json:"blockOn,omitempty"`
}

// Adapter overrides an ACP adapter command.
type Adapter struct {
	Command []string `json:"command"`
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

// SkillBackupDir returns the directory, next to the configuration, that
// keeps a copy of a skill with local edits before an update replaces it.
// It is outside every skills directory, so no harness loads a copy as a
// second slopcop skill.
func SkillBackupDir() (string, error) {
	path, err := Path()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), "skill-backups"), nil
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
	cfg.migrate()
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
	cfg.migrate()
	if cfg.Review != nil && len(cfg.Review.Reviewers) == 0 && len(cfg.Review.Fallbacks) == 0 && cfg.Review.Timeout == "" && cfg.Review.MinCompleted == 0 && len(cfg.Review.Adapters) == 0 {
		cfg.Review = nil
	}
	if cfg.Gate != nil && cfg.Gate.BlockOn == "" {
		cfg.Gate = nil
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
