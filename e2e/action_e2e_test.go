package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olegkoval/pr-automerge-github-action/internal/action"
)

type fakeGitHub struct {
	pr             map[string]any
	status         map[string]any
	checkRuns      map[string]any
	comments       []map[string]string
	createdComment string
	merged         bool
	mergeStatus    int
}

func TestActionE2E(t *testing.T) {
	t.Parallel()

	mergeable := true
	conflicted := false
	tests := []struct {
		name              string
		eventName         string
		actor             string
		mergeable         *bool
		mergeableState    string
		statusState       string
		checkConclusion   string
		mergeStatus       int
		existingComments  []map[string]string
		wantMerged        bool
		wantCommentPart   string
		wantNoComment     bool
		wantNoAPIRequired bool
	}{
		{
			name:              "exits outside pull request events",
			eventName:         "push",
			actor:             "dependabot[bot]",
			wantNoComment:     true,
			wantNoAPIRequired: true,
		},
		{
			name:            "merges allowed bot when checks pass",
			eventName:       "pull_request_target",
			actor:           "dependabot[bot]",
			mergeable:       &mergeable,
			mergeableState:  "clean",
			statusState:     "success",
			checkConclusion: "success",
			mergeStatus:     http.StatusOK,
			wantMerged:      true,
			wantNoComment:   true,
		},
		{
			name:            "ignores human pull requests",
			eventName:       "pull_request_target",
			actor:           "human-user",
			mergeable:       &mergeable,
			mergeableState:  "clean",
			statusState:     "success",
			checkConclusion: "success",
			mergeStatus:     http.StatusOK,
			wantNoComment:   true,
		},
		{
			name:            "asks dependabot to rebase on conflict",
			eventName:       "pull_request_target",
			actor:           "dependabot[bot]",
			mergeable:       &conflicted,
			mergeableState:  "dirty",
			statusState:     "success",
			checkConclusion: "success",
			mergeStatus:     http.StatusOK,
			wantCommentPart: "@dependabot rebase",
		},
		{
			name:            "mentions maintainers for other bot conflicts",
			eventName:       "pull_request_target",
			actor:           "snyk-bot",
			mergeable:       &conflicted,
			mergeableState:  "dirty",
			statusState:     "success",
			checkConclusion: "success",
			mergeStatus:     http.StatusOK,
			wantCommentPart: "@alice @bob\n\nThis maintenance bot PR has a merge conflict",
		},
		{
			name:            "mentions maintainers when checks fail",
			eventName:       "pull_request_target",
			actor:           "renovate[bot]",
			mergeable:       &mergeable,
			mergeableState:  "clean",
			statusState:     "success",
			checkConclusion: "failure",
			mergeStatus:     http.StatusOK,
			wantCommentPart: "may be a breaking dependency or security update",
		},
		{
			name:            "mentions maintainers when merge fails",
			eventName:       "pull_request_target",
			actor:           "dependabot[bot]",
			mergeable:       &mergeable,
			mergeableState:  "clean",
			statusState:     "success",
			checkConclusion: "success",
			mergeStatus:     http.StatusConflict,
			wantCommentPart: "GitHub refused to merge this maintenance bot PR",
		},
		{
			name:            "suppresses duplicate comments for same reason and head",
			eventName:       "pull_request_target",
			actor:           "dependabot[bot]",
			mergeable:       &conflicted,
			mergeableState:  "dirty",
			statusState:     "success",
			checkConclusion: "success",
			mergeStatus:     http.StatusOK,
			existingComments: []map[string]string{
				{"body": "<!-- pr-bot-automerge:dependabot-conflict:abc123 -->\n@dependabot rebase"},
			},
			wantNoComment: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fake := &fakeGitHub{
				pr: map[string]any{
					"number":          7,
					"draft":           false,
					"mergeable":       tt.mergeable,
					"mergeable_state": tt.mergeableState,
					"user":            map[string]string{"login": tt.actor},
					"head":            map[string]string{"sha": "abc123"},
				},
				status: map[string]any{"state": tt.statusState},
				checkRuns: map[string]any{"check_runs": []map[string]any{{
					"name":       "ci",
					"status":     "completed",
					"conclusion": tt.checkConclusion,
				}}},
				comments:    append([]map[string]string(nil), tt.existingComments...),
				mergeStatus: tt.mergeStatus,
			}
			server := httptest.NewServer(fake.handler(t))
			defer server.Close()

			eventPath := writeEvent(t, tt.actor)
			env := []string{
				"GITHUB_EVENT_NAME=" + tt.eventName,
				"GITHUB_EVENT_PATH=" + eventPath,
				"GITHUB_REPOSITORY=owner/repo",
				"GITHUB_API_URL=" + server.URL,
				"GITHUB_TOKEN=test-token",
				"INPUT_MAINTAINER_HANDLES=alice,bob",
			}
			var logs strings.Builder
			err := action.Run(context.Background(), env, log.New(&logs, "", 0))
			if err != nil {
				t.Fatalf("Run() error = %v\nlogs:\n%s", err, logs.String())
			}
			if tt.wantMerged != fake.merged {
				t.Fatalf("merged = %v, want %v\nlogs:\n%s", fake.merged, tt.wantMerged, logs.String())
			}
			if tt.wantNoComment && fake.createdComment != "" {
				t.Fatalf("created comment = %q, want none", fake.createdComment)
			}
			if tt.wantCommentPart != "" && !strings.Contains(fake.createdComment, tt.wantCommentPart) {
				t.Fatalf("created comment = %q, want to contain %q", fake.createdComment, tt.wantCommentPart)
			}
		})
	}
}

func (f *fakeGitHub) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/contents/.github/pr-bot-automerge.yml":
			http.NotFound(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/pulls/7":
			writeJSON(t, w, f.pr)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/commits/abc123/status":
			writeJSON(t, w, f.status)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/commits/abc123/check-runs":
			writeJSON(t, w, f.checkRuns)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues/7/comments":
			writeJSON(t, w, f.comments)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/owner/repo/issues/7/comments":
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode comment payload: %v", err)
			}
			f.createdComment = payload["body"]
			w.WriteHeader(http.StatusCreated)
			writeJSON(t, w, map[string]string{"body": f.createdComment})
		case r.Method == http.MethodPut && r.URL.Path == "/repos/owner/repo/pulls/7/merge":
			if f.mergeStatus == 0 {
				f.mergeStatus = http.StatusOK
			}
			if f.mergeStatus < 200 || f.mergeStatus > 299 {
				w.WriteHeader(f.mergeStatus)
				_, _ = fmt.Fprint(w, `{"message":"merge conflict"}`)
				return
			}
			f.merged = true
			writeJSON(t, w, map[string]bool{"merged": true})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}
}

func writeEvent(t *testing.T, actor string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "event.json")
	payload := map[string]any{
		"repository": map[string]string{"full_name": "owner/repo"},
		"pull_request": map[string]any{
			"number": 7,
			"draft":  false,
			"user":   map[string]string{"login": actor},
			"head":   map[string]string{"sha": "abc123"},
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}
