package action

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestEvaluateChecksFailsOnLaterStatusPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/project/commits/abc/status":
			switch r.URL.Query().Get("page") {
			case "1":
				_, _ = fmt.Fprint(w, `{"state":"success","total_count":2,"statuses":[{"context":"first","state":"success"}]}`)
			case "2":
				_, _ = fmt.Fprint(w, `{"state":"success","total_count":2,"statuses":[{"context":"later","state":"failure"}]}`)
			default:
				http.Error(w, "unexpected status page", http.StatusBadRequest)
			}
		case "/repos/acme/project/commits/abc/check-runs":
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newGitHubClient(server.URL, "")
	state, err := evaluateChecks(context.Background(), client, "acme/project", "abc", "", nil)
	if err != nil {
		t.Fatalf("evaluateChecks() error = %v", err)
	}
	if state != checksFailed {
		t.Fatalf("evaluateChecks() = %q, want %q", state, checksFailed)
	}
}

func TestEvaluateChecksUsesLatestStatusAndCheckRunPerName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/project/commits/abc/status":
			_, _ = fmt.Fprint(w, `{"state":"success","total_count":2,"statuses":[{"context":"ci","state":"success","created_at":"2026-10-19T08:00:00Z"},{"context":"ci","state":"failure","created_at":"2026-10-19T07:00:00Z"}]}`)
		case "/repos/acme/project/commits/abc/check-runs":
			_, _ = fmt.Fprint(w, `{"total_count":2,"check_runs":[{"id":20,"name":"ci","status":"completed","conclusion":"success"},{"id":10,"name":"ci","status":"completed","conclusion":"failure"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newGitHubClient(server.URL, "")
	state, err := evaluateChecks(context.Background(), client, "acme/project", "abc", "", nil)
	if err != nil {
		t.Fatalf("evaluateChecks() error = %v", err)
	}
	if state != checksPassed {
		t.Fatalf("evaluateChecks() = %q, want %q", state, checksPassed)
	}
}

func TestEvaluateChecksUsesLatestStatusesAndRunsAcrossPages(t *testing.T) {
	latestStatusTime := time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)
	olderStatusTime := latestStatusTime.Add(-time.Hour)
	statusPage := make([]commitStatus, 100)
	statusPage[0] = commitStatus{Context: "ci", State: "success", CreatedAt: latestStatusTime}
	for i := 1; i < len(statusPage); i++ {
		statusPage[i] = commitStatus{Context: fmt.Sprintf("other-%d", i), State: "success", CreatedAt: latestStatusTime}
	}
	statusPages := map[string]combinedStatus{
		"1": {State: "success", TotalCount: 101, Statuses: statusPage},
		"2": {State: "success", TotalCount: 101, Statuses: []commitStatus{{Context: "ci", State: "failure", CreatedAt: olderStatusTime}}},
	}
	checkPage := make([]checkRun, 10)
	for i := 0; i < 9; i++ {
		passed := "success"
		checkPage[i] = checkRun{ID: int64(100 + i), Name: fmt.Sprintf("other-%d", i), Status: "completed", Conclusion: &passed}
	}
	passed := "success"
	failed := "failure"
	checkPage[9] = checkRun{ID: 20, Name: "ci", Status: "completed", Conclusion: &passed}
	checkPages := map[string]checkRunsResponse{
		"1": {TotalCount: 11, CheckRuns: checkPage},
		"2": {TotalCount: 11, CheckRuns: []checkRun{{ID: 10, Name: "ci", Status: "completed", Conclusion: &failed}}},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var response any
		switch r.URL.Path {
		case "/repos/acme/project/commits/abc/status":
			response = statusPages[r.URL.Query().Get("page")]
		case "/repos/acme/project/commits/abc/check-runs":
			response = checkPages[r.URL.Query().Get("page")]
		default:
			http.NotFound(w, r)
			return
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer server.Close()

	client := newGitHubClient(server.URL, "")
	state, err := evaluateChecks(context.Background(), client, "acme/project", "abc", "", nil)
	if err != nil {
		t.Fatalf("evaluateChecks() error = %v", err)
	}
	if state != checksPassed {
		t.Fatalf("evaluateChecks() = %q, want %q", state, checksPassed)
	}
}
