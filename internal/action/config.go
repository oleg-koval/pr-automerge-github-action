package action

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Bots                    []string      `yaml:"bots"`
	Maintainers             []string      `yaml:"maintainers"`
	MergeMethod             string        `yaml:"merge_method"`
	DependabotRebaseComment string        `yaml:"dependabot_rebase_comment"`
	WaitTimeout             time.Duration `yaml:"wait_timeout"`
	WaitInterval            time.Duration `yaml:"wait_interval"`
	DryRun                  bool          `yaml:"dry_run"`
}

const defaultDependabotRebaseComment = "@dependabot rebase"

func loadConfig(ctx context.Context, gh *githubClient, env env, repo string) (Config, error) {
	cfg := Config{
		Bots:                    []string{"dependabot[bot]", "snyk-bot", "renovate[bot]"},
		MergeMethod:             "squash",
		DependabotRebaseComment: defaultDependabotRebaseComment,
		WaitTimeout:             30 * time.Minute,
		WaitInterval:            30 * time.Second,
	}

	configPath := valueOr(env.input("config-path"), ".github/pr-bot-automerge.yml")
	fileConfig, err := readConfigFile(ctx, gh, repo, configPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Config{}, err
	}
	cfg = mergeConfig(cfg, fileConfig)

	if value := env.input("bot-logins"); value != "" {
		cfg.Bots = splitCSV(value)
	}
	if value := env.input("maintainer-handles"); value != "" {
		cfg.Maintainers = splitCSV(value)
	}
	if value := env.input("merge-method"); value != "" {
		cfg.MergeMethod = strings.ToLower(strings.TrimSpace(value))
	}
	if value := env.input("dry-run"); value != "" {
		cfg.DryRun = strings.EqualFold(value, "true")
	}
	if value := env.input("wait-timeout"); value != "" {
		duration, err := parseDuration(value)
		if err != nil {
			return Config{}, fmt.Errorf("invalid wait-timeout: %w", err)
		}
		cfg.WaitTimeout = duration
	}
	if value := env.input("wait-interval"); value != "" {
		duration, err := parseDuration(value)
		if err != nil {
			return Config{}, fmt.Errorf("invalid wait-interval: %w", err)
		}
		cfg.WaitInterval = duration
	}
	if cfg.DependabotRebaseComment == "" {
		cfg.DependabotRebaseComment = defaultDependabotRebaseComment
	}
	if len(cfg.Bots) == 0 {
		return Config{}, errors.New("no bot logins configured")
	}
	if len(cfg.Maintainers) == 0 {
		return Config{}, errors.New("maintainer-handles input or maintainers config is required")
	}
	if cfg.MergeMethod != "merge" && cfg.MergeMethod != "squash" && cfg.MergeMethod != "rebase" {
		return Config{}, fmt.Errorf("invalid merge method %q", cfg.MergeMethod)
	}
	if cfg.WaitTimeout < 0 {
		return Config{}, errors.New("wait-timeout must be zero or positive")
	}
	if cfg.WaitInterval <= 0 {
		return Config{}, errors.New("wait-interval must be positive")
	}
	return cfg, nil
}

func readConfigFile(ctx context.Context, gh *githubClient, repo string, path string) (Config, error) {
	if data, err := os.ReadFile(path); err == nil {
		return parseConfig(data)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Config{}, err
	}
	if gh == nil || repo == "" {
		return Config{}, os.ErrNotExist
	}
	content, err := gh.getContent(ctx, repo, path)
	if err != nil {
		if errors.Is(err, errNotFound) {
			return Config{}, os.ErrNotExist
		}
		return Config{}, err
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(content.Content, "\n", ""))
	if err != nil {
		return Config{}, fmt.Errorf("decode config content: %w", err)
	}
	return parseConfig(decoded)
}

func parseConfig(data []byte) (Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config yaml: %w", err)
	}
	return cfg, nil
}

func mergeConfig(base Config, override Config) Config {
	if len(override.Bots) > 0 {
		base.Bots = override.Bots
	}
	if len(override.Maintainers) > 0 {
		base.Maintainers = override.Maintainers
	}
	if override.MergeMethod != "" {
		base.MergeMethod = override.MergeMethod
	}
	if override.DependabotRebaseComment != "" {
		base.DependabotRebaseComment = override.DependabotRebaseComment
	}
	if override.WaitTimeout != 0 {
		base.WaitTimeout = override.WaitTimeout
	}
	if override.WaitInterval != 0 {
		base.WaitInterval = override.WaitInterval
	}
	if override.DryRun {
		base.DryRun = true
	}
	return base
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(strings.TrimPrefix(part, "@"))
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func parseDuration(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	duration, err := time.ParseDuration(value)
	if err == nil {
		return duration, nil
	}
	seconds, parseErr := time.ParseDuration(value + "s")
	if parseErr == nil {
		return seconds, nil
	}
	return 0, err
}

func valueOr(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
