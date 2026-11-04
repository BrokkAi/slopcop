package cli

import (
	"fmt"
	"time"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/review"
)

// Check modes select which parts of a check run.
const (
	modeBoth   = "both"
	modeStatic = "static"
	modeAI     = "ai"
)

// resolveMode applies flag, config, and default mode precedence.
func resolveMode(flagMode string, static, ai bool, cfg config.Config) (string, error) {
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
		return modeBoth, nil
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
	settings := review.Settings{Count: review.DefaultCount, Timeout: review.DefaultTimeout}
	if cfg.Review != nil {
		if cfg.Review.Count != 0 {
			settings.Count = cfg.Review.Count
		}
		if settings.Count < review.MinCount || settings.Count > review.MaxCount {
			path, err = configPath()
			if err != nil {
				return review.Settings{}, err
			}
			return review.Settings{}, fmt.Errorf("invalid review.count in %s: want %d-%d", path, review.MinCount, review.MaxCount)
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
		if len(cfg.Review.Adapters) > 0 {
			settings.Adapters = make(map[review.Family][]string, len(cfg.Review.Adapters))
			for name, adapter := range cfg.Review.Adapters {
				family := review.Family(name)
				valid := false
				for _, supported := range review.Families {
					if family == supported {
						valid = true
						break
					}
				}
				if !valid || len(adapter.Command) == 0 || adapter.Command[0] == "" {
					path, err = configPath()
					if err != nil {
						return review.Settings{}, err
					}
					return review.Settings{}, fmt.Errorf("invalid review.adapters.%s in %s: command must be nonempty and use terra, flash, or haiku", name, path)
				}
				settings.Adapters[family] = append([]string(nil), adapter.Command...)
			}
		}
	}
	if timeoutFlag > 0 {
		settings.Timeout = timeoutFlag
	}
	return settings, nil
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
