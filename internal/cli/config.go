package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/review"
)

// configKeys lists settings supported by the config command.
const configKeys = "mode, review.reviewers, review.fallbacks, review.timeout, review.minCompleted, review.provider, review.models.ollama, review.models.openrouter, review.dsh.apiKeyFile"

// dshKeyFileKey names the file dsh reads the DeepSeek API key from.
const dshKeyFileKey = "review.dsh.apiKeyFile"

// modelKey returns the family of a review.models.<family> key, for a family
// that reviews on a model the user names.
func modelKey(key string) (review.Family, bool) {
	name, ok := strings.CutPrefix(key, "review.models.")
	if !ok || !review.TakesModel(review.Family(name)) {
		return "", false
	}
	return review.Family(name), true
}

// movedKey reports a key that an unreleased build used, naming its new key.
func movedKey(key string) error {
	if key == "review.ollamaModel" {
		return fmt.Errorf("review.ollamaModel is now review.models.ollama; run `slopcop config set review.models.ollama <model>`")
	}
	return nil
}

// newConfigCommand builds the user config commands.
func newConfigCommand() *cobra.Command {
	path, err := config.Path()
	if err != nil {
		path = "slopcop's config.json in the user config directory"
	}
	command := &cobra.Command{
		Use:   "config",
		Short: "Read or change user settings",
		Long:  "Read or change user settings. Adapter commands and gate.blockOn are edited by hand in " + path + ".",
		Args:  cobra.NoArgs,
	}
	command.AddCommand(&cobra.Command{
		Use:   "get <key>",
		Short: "Show an effective setting",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			value, err := configValue(cfg, args[0])
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), value)
			return err
		},
	})
	command.AddCommand(&cobra.Command{
		Use:   "set <key> <value>",
		Short: "Save a setting",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if err := setConfigValue(&cfg, args[0], args[1]); err != nil {
				return err
			}
			path, err := config.Save(cfg)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "slopcop: set %s to %s in %s\n", args[0], args[1], path)
			return nil
		},
	})
	command.AddCommand(&cobra.Command{
		Use:   "unset <key>",
		Short: "Restore a setting's default",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if err := unsetConfigValue(&cfg, args[0]); err != nil {
				return err
			}
			path, err := config.Save(cfg)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "slopcop: unset %s in %s\n", args[0], path)
			return nil
		},
	})
	return command
}

// configValue returns the configured or default value for a key.
func configValue(cfg config.Config, key string) (string, error) {
	if err := movedKey(key); err != nil {
		return "", err
	}
	if family, ok := modelKey(key); ok {
		if cfg.Review != nil && cfg.Review.Models[string(family)] != "" {
			return cfg.Review.Models[string(family)], nil
		}
		return "(not set)", nil
	}
	switch key {
	case "gate.blockOn":
		path, err := config.Path()
		if err != nil {
			return "", err
		}
		return "", fmt.Errorf("gate.blockOn is edited by hand in %s", path)
	case "mode":
		if cfg.Mode != "" {
			return cfg.Mode, nil
		}
		// Unset, the mode follows the reviewers and the environment; say
		// what a check here would run and why.
		mode, fallback, err := resolveMode("", false, false, cfg, os.Getenv)
		if err != nil {
			return "", err
		}
		switch fallback {
		case staticForCI:
			return mode + " (not set; a CI job runs static)", nil
		case staticForNoReviewers:
			return mode + " (not set; no reviewers are configured)", nil
		}
		return mode + " (not set)", nil
	case "review.reviewers":
		if cfg.Review != nil && len(cfg.Review.Reviewers) > 0 {
			return strings.Join(cfg.Review.Reviewers, ","), nil
		}
		return "(not set)", nil
	case "review.fallbacks":
		if cfg.Review != nil && len(cfg.Review.Fallbacks) > 0 {
			return strings.Join(cfg.Review.Fallbacks, ","), nil
		}
		return "(not set)", nil
	case "review.timeout":
		if cfg.Review != nil && cfg.Review.Timeout != "" {
			return cfg.Review.Timeout, nil
		}
		return review.DefaultTimeout.String(), nil
	case "review.minCompleted":
		if cfg.Review != nil && cfg.Review.MinCompleted > 0 {
			return strconv.Itoa(cfg.Review.MinCompleted), nil
		}
		return strconv.Itoa(review.DefaultMinCompleted) + " (not set; at most the number of reviewers)", nil
	case "review.provider":
		if cfg.Review != nil && cfg.Review.Provider != "" {
			return cfg.Review.Provider, nil
		}
		return "(not set; each family uses its own adapter)", nil
	case dshKeyFileKey:
		if keyFile := cfg.Review.DSHKeyFile(); keyFile != "" {
			return keyFile, nil
		}
		return "(not set; dsh reads DEEPSEEK_API_KEY)", nil
	default:
		return "", fmt.Errorf("unknown config key %q (valid keys: %s)", key, configKeys)
	}
}

// setConfigValue validates and updates one supported setting.
func setConfigValue(cfg *config.Config, key, value string) error {
	if err := movedKey(key); err != nil {
		return err
	}
	if family, ok := modelKey(key); ok {
		if err := review.CheckModel(family, value); err != nil {
			return fmt.Errorf("invalid %s: %w", key, err)
		}
		r := ensureReview(cfg)
		if r.Models == nil {
			r.Models = map[string]string{}
		}
		r.Models[string(family)] = value
		return nil
	}
	switch key {
	case "gate.blockOn":
		path, err := config.Path()
		if err != nil {
			return err
		}
		return fmt.Errorf("gate.blockOn is edited by hand in %s", path)
	case "mode":
		if value != modeBoth && value != modeStatic && value != modeAI {
			return fmt.Errorf("invalid mode %q (want both, static, or ai)", value)
		}
		cfg.Mode = value
	case "review.reviewers":
		names := strings.Split(value, ",")
		if len(names) > review.MaxReviewers {
			return fmt.Errorf("invalid review.reviewers %q (want 1-%d known families)", value, review.MaxReviewers)
		}
		for _, name := range names {
			if _, err := review.ParseFamily(name); err != nil {
				return fmt.Errorf("invalid review.reviewers %q: %w", value, err)
			}
		}
		if cfg.Review != nil && cfg.Review.MinCompleted > len(names) {
			return fmt.Errorf("invalid review.reviewers %q: review.minCompleted is %d; lower or unset it first", value, cfg.Review.MinCompleted)
		}
		ensureReview(cfg).Reviewers = names
	case "review.fallbacks":
		names := strings.Split(value, ",")
		if len(names) > review.MaxReviewers {
			return fmt.Errorf("invalid review.fallbacks %q (want 1-%d known families)", value, review.MaxReviewers)
		}
		for _, name := range names {
			if _, err := review.ParseFamily(name); err != nil {
				return fmt.Errorf("invalid review.fallbacks %q: %w", value, err)
			}
		}
		ensureReview(cfg).Fallbacks = names
	case "review.timeout":
		d, err := time.ParseDuration(value)
		if err != nil || d <= 0 {
			return fmt.Errorf("invalid review.timeout %q (want a positive Go duration)", value)
		}
		ensureReview(cfg).Timeout = value
	case "review.minCompleted":
		n, err := strconv.Atoi(value)
		limit := review.MaxReviewers
		if cfg.Review != nil && len(cfg.Review.Reviewers) > 0 {
			limit = len(cfg.Review.Reviewers)
		}
		if err != nil || n < 1 || n > limit {
			return fmt.Errorf("invalid review.minCompleted %q (want 1-%d, at most the number of reviewers)", value, limit)
		}
		ensureReview(cfg).MinCompleted = n
	case "review.provider":
		if value != review.ProviderCopilot {
			return fmt.Errorf("invalid review.provider %q (want %s)", value, review.ProviderCopilot)
		}
		ensureReview(cfg).Provider = value
	case dshKeyFileKey:
		keyFile, err := filepath.Abs(value)
		if err != nil {
			return fmt.Errorf("invalid %s %q: %w", key, value, err)
		}
		if err := review.CheckDSHKeyFile(keyFile); err != nil {
			return fmt.Errorf("invalid %s %q: %w", key, value, err)
		}
		ensureReview(cfg).DSH = &config.DSH{APIKeyFile: keyFile}
	default:
		return fmt.Errorf("unknown config key %q (valid keys: %s)", key, configKeys)
	}
	return nil
}

// unsetConfigValue removes a setting so its default applies again.
func unsetConfigValue(cfg *config.Config, key string) error {
	if err := movedKey(key); err != nil {
		return err
	}
	if family, ok := modelKey(key); ok {
		if cfg.Review != nil {
			delete(cfg.Review.Models, string(family))
			if cfg.Review.Empty() {
				cfg.Review = nil
			}
		}
		return nil
	}
	switch key {
	case "gate.blockOn":
		path, err := config.Path()
		if err != nil {
			return err
		}
		return fmt.Errorf("gate.blockOn is edited by hand in %s", path)
	case "mode":
		cfg.Mode = ""
	case "review.reviewers":
		if cfg.Review != nil {
			cfg.Review.Reviewers = nil
		}
	case "review.fallbacks":
		if cfg.Review != nil {
			cfg.Review.Fallbacks = nil
		}
	case "review.timeout":
		if cfg.Review != nil {
			cfg.Review.Timeout = ""
		}
	case "review.minCompleted":
		if cfg.Review != nil {
			cfg.Review.MinCompleted = 0
		}
	case "review.provider":
		if cfg.Review != nil {
			cfg.Review.Provider = ""
		}
	case dshKeyFileKey:
		if cfg.Review != nil {
			cfg.Review.DSH = nil
		}
	default:
		return fmt.Errorf("unknown config key %q (valid keys: %s)", key, configKeys)
	}
	if cfg.Review != nil && cfg.Review.Empty() {
		cfg.Review = nil
	}
	return nil
}

// ensureReview initializes and returns the review settings section.
func ensureReview(cfg *config.Config) *config.Review {
	if cfg.Review == nil {
		cfg.Review = &config.Review{}
	}
	return cfg.Review
}
