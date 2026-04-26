package action

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Bots                    []string `yaml:"bots"`
	Maintainers             []string `yaml:"maintainers"`
	MergeMethod             string   `yaml:"merge_method"`
	DependabotRebaseComment string   `yaml:"dependabot_rebase_comment"`
	DryRun                  bool     `yaml:"dry_run"`
}

const defaultDependabotRebaseComment = "@dependabot rebase"

func loadConfig(ctx context.Context, gh *githubClient, env env, repo string) (Config, error) {
	cfg := Config{
		Bots:                    []string{"dependabot[bot]", "snyk-bot", "renovate[bot]"},
		MergeMethod:             "squash",
		DependabotRebaseComment: defaultDependabotRebaseComment,
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

func valueOr(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
