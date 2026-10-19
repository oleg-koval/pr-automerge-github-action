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
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/oleg-koval/pr-automerge-github-action/internal/action"
)

type fakeGitHub struct {
	pr                        map[string]any
	prResponses               []map[string]any
	prCalls                   int
	status                    map[string]any
	ignoreCheckRunWriteErrors bool
	checkRuns                 map[string]any
	checkRunResponses         []map[string]any
	checkRunCalls             int
	comments                  []map[string]string
	createdComment            string
	merged                    bool
	mergeCalls                int
	mergePRNumbers            []int
	mergeHeadSHA              string
	mergeHeadChanged          bool
	updatedBranch             bool
	mergeStatus               int
	updateStatus              int
	openReviewComments        bool
	reviewThreadResponses     []bool
	reviewThreadCalls         int
	reviewThreadCallTimes     []time.Time
	mergeSafetyReadOrder      []string
}

func TestActionE2E(t *testing.T) {
	t.Parallel()

	mergeable := true
	conflicted := false
	tests := []struct {
		name                         string
		eventName                    string
		actor                        string
		mergeable                    *bool
		mergeableState               string
		statusState                  string
		legacyStatusCount            int
		checkConclusion              string
		mergeStatus                  int
		mergeHeadChanged             bool
		updateStatus                 int
		existingComments             []map[string]string
		wantMerged                   bool
		wantUpdatedBranch            bool
		wantCommentPart              string
		wantNoComment                bool
		wantMergeComment             bool
		includeCurrentRun            bool
		includeOldAutomerge          bool
		openReviewComments           bool
		reviewThreadResponses        []bool
		wantReviewThreadCalls        int
		wantReviewInterval           time.Duration
		wantNoAPIRequired            bool
		oversizedCheckResponse       bool
		eventHasCheckSuite           bool
		eventPRNumbers               []int
		wantMergeCalls               int
		wantMergedPRNumbers          []int
		wantError                    string
		botLoginsInput               string
		waitTimeout                  string
		waitInterval                 string
		headChangesOnRefresh         bool
		prState                      string
		prClosesAfterMergeFailure    bool
		prClosesUnmergedAfterFailure bool
		prNumber                     string
		checksChangeOnRefresh        string
		conflictHeadChanges          bool
	}{
		{
			name:              "exits outside pull request events",
			eventName:         "push",
			actor:             "dependabot[bot]",
			wantNoComment:     true,
			wantNoAPIRequired: true,
		},
		{
			name:             "accepts newline separated bot logins",
			eventName:        "pull_request_target",
			actor:            "dependabot[bot]",
			mergeable:        &mergeable,
			mergeableState:   "clean",
			statusState:      "success",
			checkConclusion:  "success",
			mergeStatus:      http.StatusOK,
			botLoginsInput:   "dependabot[bot]\nrenovate[bot]\nsnyk-bot",
			wantMerged:       true,
			wantMergeComment: true,
		},
		{
			name:             "re-evaluates scheduled PR retry",
			eventName:        "schedule",
			prNumber:         "7",
			actor:            "dependabot[bot]",
			mergeable:        &mergeable,
			mergeableState:   "clean",
			statusState:      "success",
			checkConclusion:  "success",
			mergeStatus:      http.StatusOK,
			wantMerged:       true,
			wantMergeComment: true,
		},
		{
			name:               "merges from check suite pull request payload",
			eventName:          "check_suite",
			actor:              "dependabot[bot]",
			mergeable:          &mergeable,
			mergeableState:     "clean",
			statusState:        "success",
			checkConclusion:    "success",
			mergeStatus:        http.StatusOK,
			eventHasCheckSuite: true,
			wantMerged:         true,
			wantMergeComment:   true,
		},
		{
			name:                "merges every PR from check suite payload",
			eventName:           "check_suite",
			actor:               "dependabot[bot]",
			mergeable:           &mergeable,
			mergeableState:      "clean",
			statusState:         "success",
			checkConclusion:     "success",
			mergeStatus:         http.StatusOK,
			eventHasCheckSuite:  true,
			eventPRNumbers:      []int{7, 8},
			wantMerged:          true,
			wantMergeCalls:      2,
			wantMergedPRNumbers: []int{7, 8},
			wantMergeComment:    true,
		},
		{
			name:                "processes selected check suite PR from matrix input",
			eventName:           "check_suite",
			actor:               "dependabot[bot]",
			mergeable:           &mergeable,
			mergeableState:      "clean",
			statusState:         "success",
			checkConclusion:     "success",
			mergeStatus:         http.StatusOK,
			eventHasCheckSuite:  true,
			eventPRNumbers:      []int{7, 8},
			prNumber:            "8",
			wantMerged:          true,
			wantMergeCalls:      1,
			wantMergedPRNumbers: []int{8},
			wantMergeComment:    true,
		},
		{
			name:               "rejects a PR not associated with the check suite",
			eventName:          "check_suite",
			actor:              "dependabot[bot]",
			eventHasCheckSuite: true,
			eventPRNumbers:     []int{7},
			prNumber:           "8",
			wantError:          "not associated with the check_suite event",
		},
		{
			name:             "merges allowed bot when checks pass",
			eventName:        "pull_request_target",
			actor:            "dependabot[bot]",
			mergeable:        &mergeable,
			mergeableState:   "clean",
			statusState:      "success",
			checkConclusion:  "success",
			mergeStatus:      http.StatusOK,
			wantMerged:       true,
			wantMergeComment: true,
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
			name:             "ignores empty pending legacy status",
			eventName:        "pull_request_target",
			actor:            "dependabot[bot]",
			mergeable:        &mergeable,
			mergeableState:   "clean",
			statusState:      "pending",
			checkConclusion:  "success",
			mergeStatus:      http.StatusOK,
			wantMerged:       true,
			wantMergeComment: true,
		},
		{
			name:              "ignores its own pending check run",
			eventName:         "pull_request_target",
			actor:             "dependabot[bot]",
			mergeable:         &mergeable,
			mergeableState:    "clean",
			statusState:       "success",
			checkConclusion:   "success",
			includeCurrentRun: true,
			mergeStatus:       http.StatusOK,
			wantMerged:        true,
			wantMergeComment:  true,
		},
		{
			name:                "ignores previous automerge check runs",
			eventName:           "pull_request_target",
			actor:               "dependabot[bot]",
			mergeable:           &mergeable,
			mergeableState:      "clean",
			statusState:         "success",
			checkConclusion:     "success",
			includeOldAutomerge: true,
			mergeStatus:         http.StatusOK,
			wantMerged:          true,
			wantMergeComment:    true,
		},
		{
			name:               "does not merge with open review comments",
			eventName:          "pull_request_target",
			actor:              "dependabot[bot]",
			mergeable:          &mergeable,
			mergeableState:     "clean",
			statusState:        "success",
			checkConclusion:    "success",
			mergeStatus:        http.StatusOK,
			openReviewComments: true,
			wantNoComment:      true,
		},
		{
			name:            "skips closed pull requests",
			eventName:       "pull_request_review",
			actor:           "dependabot[bot]",
			mergeable:       &mergeable,
			mergeableState:  "clean",
			statusState:     "success",
			checkConclusion: "success",
			mergeStatus:     http.StatusOK,
			prState:         "closed",
			wantNoComment:   true,
		},
		{
			name:                  "retries open review comments until resolved",
			eventName:             "pull_request_target",
			actor:                 "dependabot[bot]",
			mergeable:             &mergeable,
			mergeableState:        "clean",
			statusState:           "success",
			checkConclusion:       "success",
			mergeStatus:           http.StatusOK,
			openReviewComments:    true,
			reviewThreadResponses: []bool{true, false, false, false},
			wantReviewThreadCalls: 4,
			wantReviewInterval:    900 * time.Millisecond,
			wantMerged:            true,
			wantMergeComment:      true,
			waitTimeout:           "5s",
			waitInterval:          "1s",
		},
		{
			name:                 "skips when head changes before merge",
			eventName:            "pull_request_target",
			actor:                "dependabot[bot]",
			mergeable:            &mergeable,
			mergeableState:       "clean",
			statusState:          "success",
			checkConclusion:      "success",
			mergeStatus:          http.StatusOK,
			headChangesOnRefresh: true,
			wantMergeComment:     true,
		},
		{
			name:               "skips after review retry timeout",
			eventName:          "pull_request_target",
			actor:              "dependabot[bot]",
			mergeable:          &mergeable,
			mergeableState:     "clean",
			statusState:        "success",
			checkConclusion:    "success",
			mergeStatus:        http.StatusOK,
			openReviewComments: true,
			waitTimeout:        "10ms",
			waitInterval:       "1ms",
			wantNoComment:      true,
		},
		{
			name:              "updates branch when behind base",
			eventName:         "pull_request_target",
			actor:             "dependabot[bot]",
			mergeable:         &mergeable,
			mergeableState:    "behind",
			statusState:       "success",
			checkConclusion:   "success",
			mergeStatus:       http.StatusOK,
			updateStatus:      http.StatusAccepted,
			wantUpdatedBranch: true,
			wantNoComment:     true,
		},
		{
			name:            "asks dependabot to rebase when branch update fails",
			eventName:       "pull_request_target",
			actor:           "dependabot[bot]",
			mergeable:       &mergeable,
			mergeableState:  "behind",
			statusState:     "success",
			checkConclusion: "success",
			mergeStatus:     http.StatusOK,
			updateStatus:    http.StatusUnprocessableEntity,
			wantCommentPart: "@dependabot rebase",
		},
		{
			name:            "mentions maintainers when merge fails",
			eventName:       "pull_request_target",
			actor:           "dependabot[bot]",
			mergeable:       &mergeable,
			mergeableState:  "clean",
			statusState:     "success",
			checkConclusion: "success",
			mergeStatus:     http.StatusUnprocessableEntity,
			wantMergeCalls:  1,
			wantCommentPart: "returned 422",
		},
		{name: "checks become pending after refresh", eventName: "pull_request_target", actor: "dependabot[bot]", mergeable: &mergeable, mergeableState: "clean", statusState: "success", checkConclusion: "success", checksChangeOnRefresh: "in_progress", wantMergeComment: true},
		{name: "checks become failed after refresh", eventName: "pull_request_target", actor: "dependabot[bot]", mergeable: &mergeable, mergeableState: "clean", statusState: "success", checkConclusion: "success", checksChangeOnRefresh: "failure", wantCommentPart: "may be a breaking dependency or security update"},
		{name: "non-stale 409 escalates", eventName: "pull_request_target", actor: "dependabot[bot]", mergeable: &mergeable, mergeableState: "clean", statusState: "success", checkConclusion: "success", mergeStatus: http.StatusConflict, wantMergeCalls: 1, wantCommentPart: "GitHub refused to merge"},
		{name: "stale-head 409 skips escalation", eventName: "pull_request_target", actor: "dependabot[bot]", mergeable: &mergeable, mergeableState: "clean", statusState: "success", checkConclusion: "success", mergeStatus: http.StatusConflict, conflictHeadChanges: true, wantMergeCalls: 1, wantMergeComment: true},
		{
			name:             "skips merge when head changes after final read",
			eventName:        "pull_request_target",
			actor:            "dependabot[bot]",
			mergeable:        &mergeable,
			mergeableState:   "clean",
			statusState:      "success",
			checkConclusion:  "success",
			mergeHeadChanged: true,
			wantMergeCalls:   1,
			wantCommentPart:  "GitHub refused to merge",
		},
		{
			name:                      "treats concurrent merge as success",
			eventName:                 "pull_request_review",
			actor:                     "dependabot[bot]",
			mergeable:                 &mergeable,
			mergeableState:            "clean",
			statusState:               "success",
			checkConclusion:           "success",
			mergeStatus:               http.StatusMethodNotAllowed,
			prClosesAfterMergeFailure: true,
			wantMergeCalls:            1,
			wantMergeComment:          true,
		},
		{
			name:                         "does not treat closed unmerged PR as concurrent merge success",
			eventName:                    "pull_request_review",
			actor:                        "dependabot[bot]",
			mergeable:                    &mergeable,
			mergeableState:               "clean",
			statusState:                  "success",
			checkConclusion:              "success",
			mergeStatus:                  http.StatusMethodNotAllowed,
			prClosesUnmergedAfterFailure: true,
			wantMergeCalls:               1,
			wantCommentPart:              "GitHub refused to merge this maintenance bot PR",
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
		{
			name:                   "refuses oversized check response",
			eventName:              "pull_request_target",
			actor:                  "dependabot[bot]",
			mergeable:              &mergeable,
			mergeableState:         "clean",
			statusState:            "success",
			checkConclusion:        "success",
			oversizedCheckResponse: true,
			wantError:              "exceeded",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			checkRuns := []map[string]any{{
				"name":       "ci",
				"status":     "completed",
				"conclusion": tt.checkConclusion,
			}}
			if tt.oversizedCheckResponse {
				checkRuns = []map[string]any{{
					"name": "ci", "status": "completed", "conclusion": "success",
					"output": map[string]string{"summary": strings.Repeat("x", 33<<20)},
				}}
			}
			if tt.includeCurrentRun {
				checkRuns = append(checkRuns, map[string]any{
					"id":          321,
					"check_suite": map[string]int64{"id": 88},
					"name":        "automerge",
					"status":      "in_progress",
					"conclusion":  nil,
					"details_url": "https://github.com/owner/repo/actions/runs/123456/job/789",
				})
			}
			if tt.includeOldAutomerge {
				checkRuns = append(checkRuns, map[string]any{
					"id":          322,
					"check_suite": map[string]int64{"id": 88},
					"name":        "automerge",
					"status":      "completed",
					"conclusion":  "cancelled",
					"details_url": "https://github.com/owner/repo/actions/runs/999999/job/789",
				})
			}

			var checkRunResponses []map[string]any
			if tt.checksChangeOnRefresh != "" {
				conclusion := any(nil)
				status := "in_progress"
				if tt.checksChangeOnRefresh != "in_progress" {
					conclusion = tt.checksChangeOnRefresh
					status = "completed"
				}
				checkRunResponses = []map[string]any{{"total_count": len(checkRuns), "check_runs": checkRuns}, {"total_count": 1, "check_runs": []map[string]any{{"id": 99, "name": "ci-new", "status": status, "conclusion": conclusion}}}}
			}

			statuses := make([]map[string]any, 0, tt.legacyStatusCount)
			for i := 0; i < tt.legacyStatusCount; i++ {
				statuses = append(statuses, map[string]any{"context": "legacy", "state": tt.statusState})
			}

			fake := &fakeGitHub{
				pr: map[string]any{
					"number":          7,
					"state":           valueOrTest(tt.prState, "open"),
					"draft":           false,
					"mergeable":       tt.mergeable,
					"mergeable_state": tt.mergeableState,
					"user":            map[string]string{"login": tt.actor},
					"head":            map[string]string{"sha": "abc123"},
				},
				status:                    map[string]any{"state": tt.statusState, "total_count": len(statuses), "statuses": statuses},
				checkRuns:                 map[string]any{"total_count": len(checkRuns), "check_runs": checkRuns},
				checkRunResponses:         checkRunResponses,
				ignoreCheckRunWriteErrors: tt.oversizedCheckResponse,
				openReviewComments:        tt.openReviewComments,
				reviewThreadResponses:     append([]bool(nil), tt.reviewThreadResponses...),
				comments:                  append([]map[string]string(nil), tt.existingComments...),
				mergeStatus:               tt.mergeStatus,
				mergeHeadChanged:          tt.mergeHeadChanged,
				updateStatus:              tt.updateStatus,
			}
			fake.prResponses = []map[string]any{fake.pr}
			if tt.conflictHeadChanges {
				changedPR := make(map[string]any, len(fake.pr))
				for key, value := range fake.pr {
					changedPR[key] = value
				}
				changedPR["head"] = map[string]string{"sha": "newhead"}
				fake.prResponses = []map[string]any{fake.pr, fake.pr, changedPR}
			}
			if tt.headChangesOnRefresh {
				refreshedPR := make(map[string]any, len(fake.pr))
				for key, value := range fake.pr {
					refreshedPR[key] = value
				}
				refreshedPR["head"] = map[string]string{"sha": "def456"}
				fake.prResponses = append(fake.prResponses, refreshedPR)
			}
			if tt.prClosesAfterMergeFailure || tt.prClosesUnmergedAfterFailure {
				closedPR := make(map[string]any, len(fake.pr))
				for key, value := range fake.pr {
					closedPR[key] = value
				}
				closedPR["state"] = "closed"
				if tt.prClosesAfterMergeFailure {
					closedPR["merged"] = true
				}
				fake.prResponses = append(fake.prResponses, fake.pr, closedPR)
			}
			server := httptest.NewServer(fake.handler(t))
			defer server.Close()

			eventPath := writeEvent(t, tt.actor, tt.eventHasCheckSuite, tt.eventPRNumbers...)
			botLoginsInput := tt.botLoginsInput
			if botLoginsInput == "" {
				botLoginsInput = "dependabot[bot],renovate[bot],snyk-bot"
			}
			env := []string{
				"GITHUB_EVENT_NAME=" + tt.eventName,
				"GITHUB_EVENT_PATH=" + eventPath,
				"GITHUB_REPOSITORY=owner/repo",
				"GITHUB_API_URL=" + server.URL,
				"GITHUB_TOKEN=test-token",
				"GITHUB_RUN_ID=123456",
				"INPUT_MAINTAINER_HANDLES=alice,bob",
				"INPUT_BOT_LOGINS=" + botLoginsInput,
				"INPUT_WAIT_TIMEOUT=" + valueOrTest(tt.waitTimeout, "0s"),
				"INPUT_WAIT_INTERVAL=" + valueOrTest(tt.waitInterval, "1ms"),
				"INPUT_PR_NUMBER=" + tt.prNumber,
				"INPUT_AUTOMERGE_WORKFLOW_PATH=.github/workflows/automerge.yml",
			}
			var logs strings.Builder
			err := action.Run(context.Background(), env, log.New(&logs, "", 0))
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("Run() error = %v, want error containing %q", err, tt.wantError)
				}
				if fake.mergeCalls != 0 || fake.merged {
					t.Fatalf("oversized or failed pre-merge check reached merge endpoint: calls=%d merged=%v", fake.mergeCalls, fake.merged)
				}
				return
			}
			if err != nil {
				t.Fatalf("Run() error = %v\nlogs:\n%s", err, logs.String())
			}
			if !tt.wantMerged && tt.wantMergeCalls == 0 && fake.mergeCalls != 0 {
				t.Fatalf("negative case made %d merge API calls, want none", fake.mergeCalls)
			}
			if tt.wantMerged != fake.merged {
				t.Fatalf("merged = %v, want %v\\nlogs:\\n%s", fake.merged, tt.wantMerged, logs.String())
			}
			if fake.merged {
				lastIndex := func(want string) int {
					for index := len(fake.mergeSafetyReadOrder) - 1; index >= 0; index-- {
						if fake.mergeSafetyReadOrder[index] == want {
							return index
						}
					}
					return -1
				}
				lastReviewRead := lastIndex("review-threads")
				lastCheckRead := lastIndex("check-runs")
				lastMerge := lastIndex("merge")
				if lastReviewRead < 0 || lastCheckRead <= lastReviewRead || lastMerge <= lastCheckRead {
					t.Fatalf("final safety reads are not ordered review-threads < check-runs < merge: %v", fake.mergeSafetyReadOrder)
				}
			}
			if fake.mergeCalls > 0 && fake.mergeHeadSHA != "abc123" {
				t.Fatalf("merge head SHA = %q, want checked SHA abc123", fake.mergeHeadSHA)
			}
			if tt.wantMergeCalls != 0 && fake.mergeCalls != tt.wantMergeCalls {
				t.Fatalf("merge calls = %d, want %d", fake.mergeCalls, tt.wantMergeCalls)

			}
			if tt.wantMergedPRNumbers != nil && !slices.Equal(fake.mergePRNumbers, tt.wantMergedPRNumbers) {
				t.Fatalf("merged PR numbers = %v, want %v", fake.mergePRNumbers, tt.wantMergedPRNumbers)
			}
			if tt.wantUpdatedBranch != fake.updatedBranch {
				t.Fatalf("updatedBranch = %v, want %v\nlogs:\n%s", fake.updatedBranch, tt.wantUpdatedBranch, logs.String())
			}
			if tt.wantNoComment && fake.createdComment != "" {
				t.Fatalf("created comment = %q, want none", fake.createdComment)
			}
			if tt.wantCommentPart != "" && !strings.Contains(fake.createdComment, tt.wantCommentPart) {
				t.Fatalf("created comment = %q, want to contain %q", fake.createdComment, tt.wantCommentPart)
			}
			if tt.wantMergeComment && !strings.Contains(fake.createdComment, "Automerge checks passed for this maintenance bot PR") {
				t.Fatalf("created comment = %q, want merge approval comment", fake.createdComment)
			}
			if tt.wantMergeComment && !strings.Contains(fake.createdComment, "Managed by [oleg-koval/pr-automerge-github-action]") {
				t.Fatalf("created comment = %q, want action attribution", fake.createdComment)
			}
			if tt.wantMergeComment && !strings.Contains(fake.createdComment, "does not confirm that a merge occurred") {
				t.Fatalf("created comment = %q, want a non-final merge attempt notice", fake.createdComment)
			}
			if tt.wantReviewThreadCalls > 0 && fake.reviewThreadCalls != tt.wantReviewThreadCalls {
				t.Fatalf("reviewThreadCalls = %d, want %d", fake.reviewThreadCalls, tt.wantReviewThreadCalls)
			}
			if tt.wantReviewInterval > 0 {
				if len(fake.reviewThreadCallTimes) < 2 {
					t.Fatalf("reviewThreadCallTimes = %d, want at least 2 calls", len(fake.reviewThreadCallTimes))
				}
				if elapsed := fake.reviewThreadCallTimes[1].Sub(fake.reviewThreadCallTimes[0]); elapsed < tt.wantReviewInterval {
					t.Fatalf("review retry elapsed = %s, want at least %s", elapsed, tt.wantReviewInterval)
				}
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
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/owner/repo/pulls/"):
			prNumber, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/repos/owner/repo/pulls/"))
			if err != nil {
				t.Fatalf("parse PR number from %q: %v", r.URL.Path, err)
			}
			responseIndex := f.prCalls
			if responseIndex >= len(f.prResponses) {
				responseIndex = len(f.prResponses) - 1
			}
			f.prCalls++
			response := make(map[string]any, len(f.prResponses[responseIndex]))
			for key, value := range f.prResponses[responseIndex] {
				response[key] = value
			}
			response["number"] = prNumber
			writeJSON(t, w, response)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/commits/abc123/status":
			writeJSON(t, w, f.status)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/commits/abc123/check-runs":
			f.mergeSafetyReadOrder = append(f.mergeSafetyReadOrder, "check-runs")
			if len(f.checkRunResponses) > 0 {
				index := f.checkRunCalls
				if index >= len(f.checkRunResponses) {
					index = len(f.checkRunResponses) - 1
				}
				f.checkRunCalls++
				writeJSON(t, w, f.checkRunResponses[index])
				return
			}
			if f.ignoreCheckRunWriteErrors {
				data, err := json.Marshal(f.checkRuns)
				if err != nil {
					t.Fatalf("marshal check runs: %v", err)
				}
				_, _ = w.Write(data)
			} else {
				writeJSON(t, w, f.checkRuns)
			}
		case r.Method == http.MethodGet && (r.URL.Path == "/repos/owner/repo/actions/runs/123456" || r.URL.Path == "/repos/owner/repo/actions/runs/999999"):
			id := 123456
			if strings.HasSuffix(r.URL.Path, "999999") {
				id = 999999
			}
			writeJSON(t, w, map[string]any{"id": id, "path": ".github/workflows/automerge.yml@refs/heads/main", "head_sha": "abc123", "check_suite_id": 88, "repository": map[string]string{"full_name": "owner/repo"}})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/") && strings.HasSuffix(r.URL.Path, "/comments"):
			writeJSON(t, w, f.comments)
		case r.Method == http.MethodPost && r.URL.Path == "/graphql":
			f.mergeSafetyReadOrder = append(f.mergeSafetyReadOrder, "review-threads")
			var payload struct {
				Query string `json:"query"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode GraphQL payload: %v", err)
			}
			if !strings.Contains(payload.Query, "reviewThreads") {
				t.Fatalf("GraphQL query omitted reviewThreads: %q", payload.Query)
			}
			openReviewComments := f.openReviewComments
			if len(f.reviewThreadResponses) > 0 {
				f.reviewThreadCallTimes = append(f.reviewThreadCallTimes, time.Now())
				responseIndex := f.reviewThreadCalls
				if responseIndex >= len(f.reviewThreadResponses) {
					responseIndex = len(f.reviewThreadResponses) - 1
				}
				openReviewComments = f.reviewThreadResponses[responseIndex]
				f.reviewThreadCalls++
			}
			if openReviewComments {
				writeJSON(t, w, map[string]any{
					"data": map[string]any{
						"repository": map[string]any{
							"pullRequest": map[string]any{
								"reviewThreads": map[string]any{
									"nodes":    []map[string]bool{{"isResolved": false}},
									"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil},
								},
							},
						},
					},
				})
				return
			}
			writeJSON(t, w, map[string]any{
				"data": map[string]any{
					"repository": map[string]any{
						"pullRequest": map[string]any{
							"reviewThreads": map[string]any{
								"nodes":    []map[string]bool{{"isResolved": true}},
								"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil},
							},
						},
					},
				},
			})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/") && strings.HasSuffix(r.URL.Path, "/comments"):
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode comment payload: %v", err)
			}
			f.createdComment = payload["body"]
			w.WriteHeader(http.StatusCreated)
			writeJSON(t, w, map[string]string{"body": f.createdComment})
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/repos/owner/repo/pulls/") && strings.HasSuffix(r.URL.Path, "/merge"):
			f.mergeSafetyReadOrder = append(f.mergeSafetyReadOrder, "merge")
			f.mergeCalls++
			var mergePayload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&mergePayload); err != nil {
				t.Fatalf("decode merge payload: %v", err)
			}
			f.mergeHeadSHA = mergePayload["sha"]
			prNumber, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/owner/repo/pulls/"), "/merge"))
			if err != nil {
				t.Fatalf("parse merge PR number from %q: %v", r.URL.Path, err)
			}
			f.mergePRNumbers = append(f.mergePRNumbers, prNumber)
			if f.mergeStatus == 0 {
				f.mergeStatus = http.StatusOK
			}
			if f.mergeHeadChanged && f.mergeHeadSHA == "abc123" {
				w.WriteHeader(http.StatusConflict)
				_, _ = fmt.Fprint(w, `{"message":"Head branch was modified"}`)
				return
			}
			if f.mergeStatus < 200 || f.mergeStatus > 299 {
				w.WriteHeader(f.mergeStatus)
				_, _ = fmt.Fprint(w, `{"message":"merge conflict"}`)
				return
			}
			f.merged = true
			writeJSON(t, w, map[string]bool{"merged": true})
		case r.Method == http.MethodPut && r.URL.Path == "/repos/owner/repo/pulls/7/update-branch":
			if f.updateStatus == 0 {
				f.updateStatus = http.StatusAccepted
			}
			if f.updateStatus < 200 || f.updateStatus > 299 {
				w.WriteHeader(f.updateStatus)
				_, _ = fmt.Fprint(w, `{"message":"branch update failed"}`)
				return
			}
			f.updatedBranch = true
			w.WriteHeader(f.updateStatus)
			writeJSON(t, w, map[string]string{"message": "Updating pull request branch."})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}
}

func writeEvent(t *testing.T, actor string, checkSuite bool, numbers ...int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "event.json")
	makePullRequest := func(number int) map[string]any {
		return map[string]any{
			"number": number,
			"draft":  false,
			"user":   map[string]string{"login": actor},
			"head":   map[string]string{"sha": "abc123"},
		}
	}
	number := 7
	if len(numbers) > 0 {
		number = numbers[0]
	}
	pullRequest := makePullRequest(number)
	payload := map[string]any{
		"repository":   map[string]string{"full_name": "owner/repo"},
		"pull_request": pullRequest,
	}
	if checkSuite {
		pullRequests := make([]map[string]any, 0, len(numbers))
		if len(numbers) == 0 {
			numbers = []int{7}
		}
		for _, prNumber := range numbers {
			pullRequests = append(pullRequests, makePullRequest(prNumber))
		}
		payload = map[string]any{
			"repository":  map[string]string{"full_name": "owner/repo"},
			"check_suite": map[string]any{"pull_requests": pullRequests},
		}
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

func valueOrTest(value string, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}
