package action

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigAutomergeWorkflowPath(t *testing.T) {
	for _, tt := range []struct {
		name, workflowRef, input, configured, want string
		wantInvalid                                bool
	}{
		{name: "derive from branch", workflowRef: "acme/project/.github/workflows/automerge.yml@refs/heads/main", want: ".github/workflows/automerge.yml"},
		{name: "derive from tag", workflowRef: "acme/project/.github/workflows/merge.yaml@refs/tags/v1", want: ".github/workflows/merge.yaml"},
		{name: "preserve input", workflowRef: "acme/project/.github/workflows/automerge.yml@refs/heads/main", input: ".github/workflows/explicit.yml", want: ".github/workflows/explicit.yml"},
		{name: "preserve config", workflowRef: "acme/project/.github/workflows/automerge.yml@refs/heads/main", configured: ".github/workflows/config.yml", want: ".github/workflows/config.yml"},
		{name: "input overrides config", workflowRef: "acme/project/.github/workflows/automerge.yml@refs/heads/main", input: ".github/workflows/explicit.yml", configured: ".github/workflows/config.yml", want: ".github/workflows/explicit.yml"},
		{name: "no workflow ref"},
		{name: "unrecognized workflow ref", workflowRef: "acme/project/other.yml@refs/heads/main"},
		{name: "validate derived extension", workflowRef: "acme/project/.github/workflows/automerge.txt@refs/heads/main", wantInvalid: true},
		{name: "validate derived traversal", workflowRef: "acme/project/.github/workflows/../automerge.yml@refs/heads/main", wantInvalid: true},
		{name: "validate explicit path", workflowRef: "acme/project/.github/workflows/automerge.yml@refs/heads/main", input: "invalid.yml", wantInvalid: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.yml")
			if tt.configured != "" {
				if err := os.WriteFile(configPath, []byte("automerge_workflow_path: "+tt.configured+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := loadConfig(context.Background(), nil, env{
				"INPUT_CONFIG-PATH":             configPath,
				"INPUT_MAINTAINER-HANDLES":      "maintainer",
				"INPUT_AUTOMERGE-WORKFLOW-PATH": tt.input,
				"GITHUB_WORKFLOW_REF":           tt.workflowRef,
			}, "")
			if tt.wantInvalid {
				if err == nil || !strings.Contains(err.Error(), "invalid automerge-workflow-path") {
					t.Fatalf("loadConfig() error = %v, want invalid workflow path", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("loadConfig() error = %v", err)
			}
			if cfg.AutomergeWorkflowPath != tt.want {
				t.Fatalf("AutomergeWorkflowPath = %q, want %q", cfg.AutomergeWorkflowPath, tt.want)
			}
		})
	}
}

func TestLoadConfigAuthorAllowlist(t *testing.T) {
	for _, tc := range []struct {
		name, input, configured string
		wantOwner, wantBot      bool
	}{
		{name: "defaults", wantOwner: true, wantBot: true},
		{name: "input replaces defaults", input: "custom-bot"},
		{name: "config replaces defaults", configured: "bots: [custom-bot]\n"},
		{name: "input overrides config", input: "oleg-koval", configured: "bots: [custom-bot]\n", wantOwner: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yml")
			if err := os.WriteFile(path, []byte(tc.configured), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := loadConfig(context.Background(), nil, env{
				"INPUT_CONFIG-PATH":        path,
				"INPUT_MAINTAINER-HANDLES": "maintainer",
				"INPUT_BOT-LOGINS":         tc.input,
			}, "")
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "defaults" {
				wantBots := []string{"dependabot[bot]", "snyk-bot", "renovate[bot]", "oleg-koval"}
				if len(cfg.Bots) != len(wantBots) {
					t.Fatalf("default bot allowlist = %v, want %v", cfg.Bots, wantBots)
				}
				for _, bot := range wantBots {
					if !containsLogin(cfg.Bots, bot) {
						t.Fatalf("default bot allowlist = %v, want %v", cfg.Bots, wantBots)
					}
				}
			}
			if containsLogin(cfg.Bots, "oleg-koval") != tc.wantOwner || containsLogin(cfg.Bots, "dependabot[bot]") != tc.wantBot {
				t.Fatalf("unexpected author allowlist: %v", cfg.Bots)
			}
		})
	}
}
