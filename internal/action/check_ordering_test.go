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

func TestEvaluateChecksLatestFailureAndPendingAcrossPages(t *testing.T) {
	latestTime := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	olderTime := latestTime.Add(-time.Hour)
	for _, tt := range []struct {
		name         string
		want         checkState
		differentApp bool
	}{
		{name: "latest status failure on later page", want: checksFailed},
		{name: "latest status pending on later page", want: checksPending},
		{name: "latest check-run failure on later page", want: checksFailed},
		{name: "distinct check app failure is preserved across pages", want: checksFailed, differentApp: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			statuses := make([]commitStatus, 100)
			for index := range statuses {
				statuses[index] = commitStatus{Context: fmt.Sprintf("other-%d", index), State: "success", CreatedAt: latestTime}
			}
			statuses[0] = commitStatus{ID: 1, Context: "ci", State: "success", CreatedAt: olderTime, Creator: ghUser{ID: 7}}
			latestState := "failure"
			if tt.name == "latest status pending on later page" {
				latestState = "pending"
			}
			statusPages := map[string]combinedStatus{
				"1": {State: "success", TotalCount: 101, Statuses: statuses},
				"2": {State: "success", TotalCount: 101, Statuses: []commitStatus{{ID: 2, Context: "ci", State: latestState, CreatedAt: latestTime, Creator: ghUser{ID: 7}}}},
			}
			checkPages := map[string]checkRunsResponse{"1": {TotalCount: 0}}
			if tt.name == "latest check-run failure on later page" || tt.differentApp {
				statusPages = map[string]combinedStatus{"1": {State: "success", TotalCount: 0}}
				checks := make([]checkRun, 100)
				for index := range checks {
					checks[index] = checkRun{ID: int64(100 + index), Name: fmt.Sprintf("other-%d", index), Status: "completed", Conclusion: stringPointer("success")}
				}
				checks[99] = checkRun{ID: 10, Name: "ci", Status: "completed", Conclusion: stringPointer("success"), App: &ghApp{ID: 15}}
				secondConclusion := "failure"
				secondApp := int64(15)
				if tt.differentApp {
					checks[99].Conclusion = stringPointer("failure")
					secondConclusion = "success"
					secondApp = 16
				}
				checkPages = map[string]checkRunsResponse{
					"1": {TotalCount: 101, CheckRuns: checks},
					"2": {TotalCount: 101, CheckRuns: []checkRun{{ID: 20, Name: "ci", Status: "completed", Conclusion: &secondConclusion, App: &ghApp{ID: secondApp}}}},
				}
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
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			state, err := evaluateChecks(context.Background(), newGitHubClient(server.URL, ""), "acme/project", "abc", "", nil)
			if err != nil {
				t.Fatalf("evaluateChecks() error = %v", err)
			}
			if state != tt.want {
				t.Fatalf("evaluateChecks() = %q, want %q", state, tt.want)
			}
		})
	}
}

func TestEvaluateChecksUsesStatusIDForSameTimestampTie(t *testing.T) {
	createdAt := "2026-10-19T09:00:00Z"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/project/commits/abc/status":
			_, _ = fmt.Fprintf(w, `{"state":"success","total_count":2,"statuses":[{"id":1,"context":"ci","state":"failure","created_at":%q,"creator":{"id":7}},{"id":2,"context":"ci","state":"success","created_at":%q,"creator":{"id":7}}]}`, createdAt, createdAt)
		case "/repos/acme/project/commits/abc/check-runs":
			_, _ = fmt.Fprint(w, `{"total_count":2,"check_runs":[{"id":10,"name":"ci","status":"completed","conclusion":"failure","app":{"id":15}},{"id":20,"name":"ci","status":"completed","conclusion":"success","app":{"id":15}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	state, err := evaluateChecks(context.Background(), newGitHubClient(server.URL, ""), "acme/project", "abc", "", nil)
	if err != nil {
		t.Fatalf("evaluateChecks() error = %v", err)
	}
	if state != checksPassed {
		t.Fatalf("evaluateChecks() = %q, want %q", state, checksPassed)
	}
}
