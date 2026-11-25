package cli

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/report"
	"github.com/BrokkAi/slopcop/internal/review"
)

// Check modes select which parts of a check run.
const (
	modeBoth   = "both"
	modeStatic = "static"
	modeAI     = "ai"
)

// Reasons a check with no mode set runs only the static half.
const (
	staticForCI          = "CI"
	staticForNoReviewers = "no reviewers"
)

// resolveMode applies flag, config, and default mode precedence. A mode a
// person chose, by flag or in the config file, is used as is. With none, the
// check runs both halves only where the review can run: reviewers are
// configured and it is not a CI job. Otherwise it runs the static half, which
// is what a check did before the review existed, so a CI job or a machine
// that never ran slopcop init keeps its exit code. fallback names why.
func resolveMode(flagMode string, static, ai bool, cfg config.Config, getenv func(string) string) (mode, fallback string, err error) {
	mode, err = chosenMode(flagMode, static, ai, cfg)
	if err != nil || mode != "" {
		return mode, "", err
	}
	if inCI(getenv) {
		return modeStatic, staticForCI, nil
	}
	if cfg.Review == nil || len(cfg.Review.Reviewers) == 0 {
		return modeStatic, staticForNoReviewers, nil
	}
	return modeBoth, "", nil
}

// reviewSkippedReason names, for the report, why a static-only check did
// not run the local review, from the fallback resolveMode gave.
func reviewSkippedReason(fallback string) string {
	switch fallback {
	case staticForCI:
		return report.ReviewSkippedCI
	case staticForNoReviewers:
		return report.ReviewSkippedNoReviewers
	}
	return report.ReviewSkippedStaticMode
}

// inCI reports whether the check runs as a CI job. Most CI services set CI;
// Azure Pipelines sets TF_BUILD and Jenkins sets JENKINS_URL.
func inCI(getenv func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(getenv("CI"))) {
	case "", "0", "false", "no":
	default:
		return true
	}
	return strings.TrimSpace(getenv("TF_BUILD")) != "" || strings.TrimSpace(getenv("JENKINS_URL")) != ""
}

// chosenMode is the mode a flag or the config file names, or "" for none.
func chosenMode(flagMode string, static, ai bool, cfg config.Config) (string, error) {
	if static && ai {
		return "", fmt.Errorf("--static and --ai cannot be used together")
	}
	if flagMode != "" && (static || ai) {
		return "", fmt.Errorf("--mode cannot be combined with --static or --ai")
	}
	mode, source := cfg.Mode, ""
	if mode != "" {
		path, err := config.Path()
		if err != nil {
			return "", err
		}
		source = "config file " + path
	}
	if static {
		mode, source = modeStatic, "--static flag"
	}
	if ai {
		mode, source = modeAI, "--ai flag"
	}
	if flagMode != "" {
		mode, source = flagMode, "--mode flag"
	}
	if mode == "" {
		return "", nil
	}
	switch mode {
	case modeBoth, modeStatic, modeAI:
		return mode, nil
	}
	return "", fmt.Errorf("invalid mode %q from %s (want both, static, or ai)", mode, source)
}

// reviewSettings resolves configured reviewer limits and adapter commands.
func reviewSettings(cfg config.Config, timeoutFlag time.Duration) (review.Settings, error) {
	path := ""
	configPath := func() (string, error) {
		if path != "" {
			return path, nil
		}
		var err error
		path, err = config.Path()
		return path, err
	}
	var err error
	settings := review.Settings{Timeout: review.DefaultTimeout}
	if cfg.Review != nil {
		if len(cfg.Review.Reviewers) > review.MaxReviewers {
			path, err = configPath()
			if err != nil {
				return review.Settings{}, err
			}
			return review.Settings{}, fmt.Errorf("invalid review.reviewers in %s: want 1-%d families", path, review.MaxReviewers)
		}
		for _, name := range cfg.Review.Reviewers {
			family, parseErr := review.ParseFamily(name)
			if parseErr != nil {
				path, err = configPath()
				if err != nil {
					return review.Settings{}, err
				}
				return review.Settings{}, fmt.Errorf("invalid review.reviewers in %s: %w", path, parseErr)
			}
			settings.Reviewers = append(settings.Reviewers, family)
		}
		if len(cfg.Review.Fallbacks) > review.MaxReviewers {
			path, err = configPath()
			if err != nil {
				return review.Settings{}, err
			}
			return review.Settings{}, fmt.Errorf("invalid review.fallbacks in %s: want at most %d families", path, review.MaxReviewers)
		}
		for _, name := range cfg.Review.Fallbacks {
			family, parseErr := review.ParseFamily(name)
			if parseErr != nil {
				path, err = configPath()
				if err != nil {
					return review.Settings{}, err
				}
				return review.Settings{}, fmt.Errorf("invalid review.fallbacks in %s: %w", path, parseErr)
			}
			settings.Fallbacks = append(settings.Fallbacks, family)
		}
		if cfg.Review.Timeout != "" {
			settings.Timeout, err = time.ParseDuration(cfg.Review.Timeout)
			if err != nil || settings.Timeout <= 0 {
				path, err = configPath()
				if err != nil {
					return review.Settings{}, err
				}
				return review.Settings{}, fmt.Errorf("invalid review.timeout in %s: want a positive Go duration", path)
			}
		}
		if n := cfg.Review.MinCompleted; n != 0 {
			if n < 1 || len(settings.Reviewers) > 0 && n > len(settings.Reviewers) {
				path, err = configPath()
				if err != nil {
					return review.Settings{}, err
				}
				return review.Settings{}, fmt.Errorf("invalid review.minCompleted in %s: want 1 to the number of reviewers (%d)", path, len(settings.Reviewers))
			}
			settings.MinCompleted = n
		}
		switch cfg.Review.Provider {
		case "":
		case review.ProviderCopilot:
			settings.Provider = review.ProviderCopilot
		default:
			path, err = configPath()
			if err != nil {
				return review.Settings{}, err
			}
			return review.Settings{}, fmt.Errorf("invalid review.provider %q in %s: want %s", cfg.Review.Provider, path, review.ProviderCopilot)
		}
		if len(cfg.Review.Adapters) > 0 {
			settings.Adapters = make(map[string][]string, len(cfg.Review.Adapters))
			for name, adapter := range cfg.Review.Adapters {
				if (name != "codex" && name != "dsh" && name != "claude" && name != review.ProviderCopilot && name != review.AdapterName(review.Gemini) && name != review.AdapterName(review.Kimi) && name != review.AdapterName(review.Grok) && name != review.AdapterName(review.Ollama) && !review.IsChatAdapter(name)) || len(adapter.Command) == 0 || adapter.Command[0] == "" {
					path, err = configPath()
					if err != nil {
						return review.Settings{}, err
					}
					return review.Settings{}, fmt.Errorf("invalid review.adapters.%s in %s: command must be nonempty and use codex, dsh, claude, copilot, antigravity, kimi, grok, opencode, or openrouter", name, path)
				}
				settings.Adapters[name] = append([]string(nil), adapter.Command...)
			}
		}
		if keyFile := cfg.Review.DSHKeyFile(); keyFile != "" {
			if !filepath.IsAbs(keyFile) {
				path, err = configPath()
				if err != nil {
					return review.Settings{}, err
				}
				return review.Settings{}, fmt.Errorf("invalid review.dsh.apiKeyFile in %s: want an absolute path", path)
			}
			settings.DSHKeyFile = keyFile
		}
		if err = chatModels(cfg.Review, &settings, configPath); err != nil {
			return review.Settings{}, err
		}
	}
	if timeoutFlag > 0 {
		settings.Timeout = timeoutFlag
	}
	return settings, nil
}

// chatModels reads review.models and requires a model for each chat family
// the review names, since such a family has no model of its own. Ollama
// without a model is reported when its reviewer starts.
func chatModels(cfg *config.Review, settings *review.Settings, configPath func() (string, error)) error {
	invalid := func(format string, args ...any) error {
		path, err := configPath()
		if err != nil {
			return err
		}
		return fmt.Errorf("invalid review.models in %s: %s", path, fmt.Sprintf(format, args...))
	}
	for name, model := range cfg.Models {
		family := review.Family(name)
		if err := review.CheckModel(family, model); err != nil {
			return invalid("%v", err)
		}
		if settings.Models == nil {
			settings.Models = make(map[review.Family]string, len(cfg.Models))
		}
		settings.Models[family] = model
	}
	for _, family := range slices.Concat(settings.Reviewers, settings.Fallbacks) {
		if review.IsChatAdapter(string(family)) && settings.Models[family] == "" {
			return invalid("%s needs a model: run `slopcop config set review.models.%s vendor/model`", family, family)
		}
	}
	return nil
}

// pushBlockLevel reads the human-owned approval threshold.
func pushBlockLevel(cfg config.Config) (string, error) {
	if cfg.Gate == nil || cfg.Gate.BlockOn == "" {
		return "error", nil
	}
	switch cfg.Gate.BlockOn {
	case "never", "finding", "note", "warning", "error":
		return cfg.Gate.BlockOn, nil
	}
	path, err := config.Path()
	if err != nil {
		return "", err
	}
	return "", fmt.Errorf("invalid gate.blockOn in %s: want never, finding, note, warning, or error", path)
}
