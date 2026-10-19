package action

import (
	"context"
	"encoding/json"

	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEveryDistinctStatusAndRunMustPass(t *testing.T) {
	for _, tt := range []struct {
		name     string
		statuses []commitStatus
		runs     []checkRun
		want     checkState
	}{
		{"same creator old failure", []commitStatus{{ID: 1, Context: "ci", State: "failure", Creator: ghUser{ID: 7}}, {ID: 2, Context: "ci", State: "success", Creator: ghUser{ID: 7}}}, nil, checksFailed},
		{"different creator old failure", []commitStatus{{ID: 1, Context: "ci", State: "failure", Creator: ghUser{ID: 7}}, {ID: 2, Context: "ci", State: "success", Creator: ghUser{ID: 8}}}, nil, checksFailed},
		{"same app old run failure", nil, []checkRun{{ID: 1, Name: "ci", Status: "completed", Conclusion: stringPointer("failure"), App: &ghApp{ID: 7}}, {ID: 2, Name: "ci", Status: "completed", Conclusion: stringPointer("success"), App: &ghApp{ID: 7}}}, checksFailed},
		{"failure outranks pending", []commitStatus{{ID: 1, Context: "ci", State: "pending"}}, []checkRun{{ID: 2, Name: "other", Status: "completed", Conclusion: stringPointer("failure")}}, checksFailed},
		{"pending run", []commitStatus{{ID: 1, Context: "ci", State: "success"}}, []checkRun{{ID: 2, Name: "other", Status: "in_progress"}}, checksPending},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/acme/project/commits/abc/status":
					_ = json.NewEncoder(w).Encode(combinedStatus{State: "success", TotalCount: len(tt.statuses), Statuses: tt.statuses})
				case "/repos/acme/project/commits/abc/check-runs":
					_ = json.NewEncoder(w).Encode(checkRunsResponse{TotalCount: len(tt.runs), CheckRuns: tt.runs})
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			got, err := evaluateChecks(context.Background(), newGitHubClient(server.URL, ""), "acme/project", "abc", nil, "")
			if err != nil || got != tt.want {
				t.Fatalf("state=%s err=%v want %s", got, err, tt.want)
			}
		})
	}
}

func TestLaterPage404IsNotOptional(t *testing.T) {
	for _, endpoint := range []string{"status", "check-runs"} {
		t.Run(endpoint, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("page") == "2" {
					http.NotFound(w, r)
					return
				}
				if r.URL.Path == "/repos/acme/project/commits/abc/status" {
					if endpoint == "status" {
						_ = json.NewEncoder(w).Encode(combinedStatus{TotalCount: 2, Statuses: []commitStatus{{ID: 1, Context: "ci", State: "success"}}})
					} else {
						http.NotFound(w, r)
					}
					return
				}
				if r.URL.Path == "/repos/acme/project/commits/abc/check-runs" {
					if endpoint == "check-runs" {
						_ = json.NewEncoder(w).Encode(checkRunsResponse{TotalCount: 2, CheckRuns: []checkRun{{ID: 1, Name: "ci", Status: "completed", Conclusion: stringPointer("success")}}})
					} else {
						http.NotFound(w, r)
					}
					return
				}
				http.NotFound(w, r)
			}))
			defer server.Close()
			got, err := evaluateChecks(context.Background(), newGitHubClient(server.URL, ""), "acme/project", "abc", nil, "")
			if err == nil || got != checksFailed {
				t.Fatalf("later page 404: state=%s err=%v; want fatal", got, err)
			}
		})
	}
}

func TestSameCountChangingRecordsFailsClosed(t *testing.T) {
	for _, endpoint := range []string{"status", "check-runs"} {
		t.Run(endpoint, func(t *testing.T) {
			firstPageCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/repos/acme/project/commits/abc/status" && endpoint == "status" {
					if r.URL.Query().Get("page") == "1" {
						firstPageCalls++
						state := "success"
						if firstPageCalls > 1 {
							state = "failure"
						}
						_ = json.NewEncoder(w).Encode(combinedStatus{TotalCount: 2, Statuses: []commitStatus{{ID: 1, Context: "ci", State: state}}})
					} else {
						_ = json.NewEncoder(w).Encode(combinedStatus{TotalCount: 2, Statuses: []commitStatus{{ID: 2, Context: "other", State: "success"}}})
					}
					return
				}
				if r.URL.Path == "/repos/acme/project/commits/abc/check-runs" && endpoint == "check-runs" {
					if r.URL.Query().Get("page") == "1" {
						firstPageCalls++
						state := "success"
						if firstPageCalls > 1 {
							state = "failure"
						}
						_ = json.NewEncoder(w).Encode(checkRunsResponse{TotalCount: 2, CheckRuns: []checkRun{{ID: 1, Name: "ci", Status: "completed", Conclusion: &state}}})
					} else {
						_ = json.NewEncoder(w).Encode(checkRunsResponse{TotalCount: 2, CheckRuns: []checkRun{{ID: 2, Name: "other", Status: "completed", Conclusion: stringPointer("success")}}})
					}
					return
				}
				http.NotFound(w, r)
			}))
			defer server.Close()
			got, err := evaluateChecks(context.Background(), newGitHubClient(server.URL, ""), "acme/project", "abc", nil, "")
			if err != nil || got != checksPending || firstPageCalls < 2 {
				t.Fatalf("state=%s err=%v scans=%d; want pending after two scans", got, err, firstPageCalls)
			}
		})
	}
}

func TestIgnoredCheckRequiresVerifiedWorkflowRun(t *testing.T) {
	for _, tt := range []struct {
		name, runPath, runSHA, runRepo, details string
		suite                                   int64
		want                                    checkState
	}{
		{"matching workflow", ".github/workflows/automerge.yml", "abc", "acme/project", "https://github.com/acme/project/actions/runs/123/job/42", 88, checksPassed},
		{"unrelated workflow", ".github/workflows/other.yml", "abc", "acme/project", "https://github.com/acme/project/actions/runs/123/job/42", 88, checksFailed},
		{"wrong head", ".github/workflows/automerge.yml", "other", "acme/project", "https://github.com/acme/project/actions/runs/123/job/42", 88, checksFailed},
		{"wrong repo", ".github/workflows/automerge.yml", "abc", "other/repo", "https://github.com/acme/project/actions/runs/123/job/42", 88, checksFailed},
		{"missing suite", ".github/workflows/automerge.yml", "abc", "acme/project", "https://github.com/acme/project/actions/runs/123/job/42", 0, checksFailed},
		{"wrong suite", ".github/workflows/automerge.yml", "abc", "acme/project", "https://github.com/acme/project/actions/runs/123/job/42", 89, checksFailed},
		{"forged run URL", ".github/workflows/automerge.yml", "abc", "acme/project", "https://github.com/other/repo/actions/runs/123/job/42", 88, checksFailed},
		{"current run from unrelated workflow", ".github/workflows/other.yml", "abc", "acme/project", "https://github.com/acme/project/actions/runs/123/job/42", 88, checksFailed},
		{"expected workflow with ignored name", ".github/workflows/automerge.yml", "abc", "acme/project", "https://github.com/acme/project/actions/runs/123/job/42", 88, checksPassed},
		{"expected workflow unlisted check name is not ignored", ".github/workflows/automerge.yml", "abc", "acme/project", "https://github.com/acme/project/actions/runs/123/job/42", 88, checksFailed},
		{"no expected path", ".github/workflows/automerge.yml", "abc", "acme/project", "https://github.com/acme/project/actions/runs/123/job/42", 88, checksFailed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/acme/project/commits/abc/status":
					_, _ = fmt.Fprint(w, `{"state":"success","total_count":1,"statuses":[{"id":5,"context":"ci","state":"success"}]}`)
				case "/repos/acme/project/commits/abc/check-runs":
					name := "automerge"
					if tt.name == "expected workflow unlisted check name is not ignored" {
						name = "otherwise-required"
					}
					_ = json.NewEncoder(w).Encode(checkRunsResponse{TotalCount: 1, CheckRuns: []checkRun{{ID: 21, Name: name, Status: "completed", Conclusion: stringPointer("failure"), DetailsURL: tt.details, CheckSuite: &ghCheckSuite{ID: tt.suite}}}})
				case "/repos/acme/project/actions/runs/123":
					_ = json.NewEncoder(w).Encode(map[string]any{"id": 123, "path": tt.runPath, "head_sha": tt.runSHA, "repository": map[string]string{"full_name": tt.runRepo}, "check_suite_id": 88})
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			expected := ".github/workflows/automerge.yml"
			if tt.name == "no expected path" {
				expected = ""
			}
			got, err := evaluateChecks(context.Background(), newGitHubClient(server.URL, ""), "acme/project", "abc", []string{"automerge"}, expected)
			if err != nil || got != tt.want {
				t.Fatalf("state=%s err=%v want %s", got, err, tt.want)
			}
		})
	}
}
