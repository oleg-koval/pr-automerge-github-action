package e2e_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type workflowDefinition struct {
	Jobs map[string]struct {
		Steps []struct {
			ID  string `yaml:"id"`
			Run string `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func TestWorkflowDiscoveryScriptsFanOutSafely(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Dir(filepath.Dir(sourceFile))
	workflows := []string{
		filepath.Join(root, ".github/workflows/automerge.yml"),
		filepath.Join(root, "examples/workflow.yml"),
	}
	for _, workflowPath := range workflows {
		workflowPath := workflowPath
		t.Run(filepath.Base(filepath.Dir(workflowPath))+"/"+filepath.Base(workflowPath), func(t *testing.T) {
			script := readDiscoveryScript(t, workflowPath)
			for _, tc := range []struct {
				name        string
				event       string
				numbers     int
				empty       bool
				fail        bool
				wantFailure bool
			}{
				{name: "schedule preserves all records across pagination boundary", event: "schedule", numbers: 101},
				{name: "blank manual dispatch enumerates open PRs", event: "workflow_dispatch", numbers: 3},
				{name: "255 matrix jobs plus discover job fit the workflow limit", event: "schedule", numbers: 255},
				{name: "256 matrix jobs plus discover job exceed the workflow limit", event: "schedule", numbers: 256, wantFailure: true},
				{name: "over-limit matrix fails without output", event: "schedule", numbers: 257, wantFailure: true},
				{name: "empty result writes empty matrix", event: "schedule", empty: true},
				{name: "later page failure fails closed", event: "schedule", numbers: 101, fail: true, wantFailure: true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					outputPath := filepath.Join(t.TempDir(), "github-output")
					fixtureNumbers := newlineNumbers(tc.numbers)
					if tc.empty {
						fixtureNumbers = ""
					}
					path := createFakeGH(t)
					cmd := exec.Command("bash", "-e", "-u", "-o", "pipefail", "-c", script)
					cmd.Env = append(os.Environ(),
						"PATH="+path+string(os.PathListSeparator)+os.Getenv("PATH"),
						"EVENT_NAME="+tc.event,
						"REQUESTED_PR_NUMBER=",
						"GITHUB_EVENT_PATH="+writeEmptyEvent(t),
						"GITHUB_REPOSITORY=owner/repo",
						"GITHUB_OUTPUT="+outputPath,
						"GH_TOKEN=test-token",
						"GH_FIXTURE_NUMBERS="+fixtureNumbers,
						fmt.Sprintf("GH_FIXTURE_FAIL=%t", tc.fail),
					)
					combinedOutput, err := cmd.CombinedOutput()
					if tc.wantFailure {
						if err == nil {
							t.Fatalf("discovery succeeded, want failure; output: %s", combinedOutput)
						}
						if _, statErr := os.Stat(outputPath); !os.IsNotExist(statErr) {
							contents, _ := os.ReadFile(outputPath)
							t.Fatalf("failed discovery left matrix output %q (stat error %v)", contents, statErr)
						}
						return
					}
					if err != nil {
						t.Fatalf("discovery failed: %v\n%s", err, combinedOutput)
					}
					contents, err := os.ReadFile(outputPath)
					if err != nil {
						t.Fatalf("read GITHUB_OUTPUT: %v", err)
					}
					want := "pr-numbers=" + jsonNumberArray(tc.numbers) + "\n"
					if tc.empty {
						want = "pr-numbers=[]\n"
					}
					if string(contents) != want {
						t.Fatalf("GITHUB_OUTPUT = %q, want %q", contents, want)
					}
				})
			}
		})
	}
}

func readDiscoveryScript(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read workflow %s: %v", path, err)
	}
	var workflow workflowDefinition
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("parse workflow %s: %v", path, err)
	}
	for _, step := range workflow.Jobs["discover"].Steps {
		if step.ID == "list" && strings.TrimSpace(step.Run) != "" {
			return step.Run
		}
	}
	t.Fatalf("workflow %s has no discover/list run script", path)
	return ""
}

func createFakeGH(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gh")
	const fake = `#!/bin/sh
[ "$1" = "api" ] || exit 90
shift
[ "$1" = "--paginate" ] || exit 91
shift
[ "$1" = "repos/owner/repo/pulls?state=open&per_page=100" ] || exit 92
shift
[ "$1" = "--jq" ] || exit 93
[ "$2" = ".[].number" ] || exit 94
[ "$GH_FIXTURE_FAIL" != "true" ] || exit 1
printf '%s' "$GH_FIXTURE_NUMBERS"
`
	if err := os.WriteFile(path, []byte(fake), 0o700); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	return dir
}

func writeEmptyEvent(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write event fixture: %v", err)
	}
	return path
}

func newlineNumbers(count int) string {
	if count == 0 {
		return ""
	}
	values := make([]string, count)
	for i := range values {
		values[i] = fmt.Sprint(i + 1)
	}
	return strings.Join(values, "\n") + "\n"
}

func jsonNumberArray(count int) string {
	if count == 0 {
		return "[]"
	}
	values := make([]string, count)
	for i := range values {
		values[i] = fmt.Sprint(i + 1)
	}
	return "[" + strings.Join(values, ",") + "]"
}
