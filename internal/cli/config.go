package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/review"
)

// configKeys lists settings supported by the config command.
const configKeys = "mode, review.reviewers, review.timeout"

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
	case "review.timeout":
		if cfg.Review != nil && cfg.Review.Timeout != "" {
			return cfg.Review.Timeout, nil
		}
		return review.DefaultTimeout.String(), nil
	default:
		return "", fmt.Errorf("unknown config key %q (valid keys: %s)", key, configKeys)
	}
}

// setConfigValue validates and updates one supported setting.
func setConfigValue(cfg *config.Config, key, value string) error {
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
		ensureReview(cfg).Reviewers = names
	case "review.timeout":
		d, err := time.ParseDuration(value)
		if err != nil || d <= 0 {
			return fmt.Errorf("invalid review.timeout %q (want a positive Go duration)", value)
		}
		ensureReview(cfg).Timeout = value
	default:
		return fmt.Errorf("unknown config key %q (valid keys: %s)", key, configKeys)
	}
	return nil
}

// unsetConfigValue removes a setting so its default applies again.
func unsetConfigValue(cfg *config.Config, key string) error {
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
	case "review.timeout":
		if cfg.Review != nil {
			cfg.Review.Timeout = ""
		}
	default:
		return fmt.Errorf("unknown config key %q (valid keys: %s)", key, configKeys)
	}
	if cfg.Review != nil && len(cfg.Review.Reviewers) == 0 && cfg.Review.Timeout == "" && len(cfg.Review.Adapters) == 0 {
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
