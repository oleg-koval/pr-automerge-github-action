package action

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetCheckRunsPaginatesUntilLastPage(t *testing.T) {
	ok := "success"
	firstPage := make([]checkRun, 100)
	for i := range firstPage {
		firstPage[i] = checkRun{Name: fmt.Sprintf("check-%d", i), Status: "completed", Conclusion: &ok}
	}
	failed := "failure"
	pages := map[string]string{
		"1": marshalTestJSON(t, checkRunsResponse{TotalCount: 101, CheckRuns: firstPage}),
		"2": marshalTestJSON(t, checkRunsResponse{TotalCount: 101, CheckRuns: []checkRun{{Name: "later", Status: "completed", Conclusion: &failed}}}),
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/repos/acme/project/commits/abc/check-runs" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("per_page") != "100" {
			t.Errorf("per_page = %q, want 100", r.URL.Query().Get("per_page"))
		}
		if body, ok := pages[r.URL.Query().Get("page")]; ok {
			_, _ = fmt.Fprint(w, body)
			return
		}
		t.Errorf("unexpected page %q", r.URL.Query().Get("page"))
		http.Error(w, "unexpected page", http.StatusBadRequest)
	}))
	defer server.Close()

	client := newGitHubClient(server.URL, "")
	runs, err := client.getCheckRuns(context.Background(), "acme/project", "abc")
	if err != nil {
		t.Fatalf("getCheckRuns() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("request count = %d, want 2 pages", calls)
	}
	if len(runs.CheckRuns) != 101 {
		t.Fatalf("check run count = %d, want 101", len(runs.CheckRuns))
	}
	if runs.CheckRuns[100].Name != "later" || runs.CheckRuns[100].Conclusion == nil || *runs.CheckRuns[100].Conclusion != "failure" {
		t.Fatalf("later-page failing check was not preserved: %#v", runs.CheckRuns[100])
	}
}

func TestGetCombinedStatusPaginatesStatuses(t *testing.T) {
	firstPage := make([]commitStatus, 100)
	for i := range firstPage {
		firstPage[i] = commitStatus{Context: fmt.Sprintf("status-%d", i), State: "success"}
	}
	pages := map[string]string{
		"1": marshalTestJSON(t, combinedStatus{State: "failure", TotalCount: 101, Statuses: firstPage}),
		"2": marshalTestJSON(t, combinedStatus{State: "failure", TotalCount: 101, Statuses: []commitStatus{{Context: "later", State: "failure"}}}),
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/repos/acme/project/commits/abc/status" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("per_page") != "100" {
			t.Errorf("per_page = %q, want 100", r.URL.Query().Get("per_page"))
		}
		if body, ok := pages[r.URL.Query().Get("page")]; ok {
			_, _ = fmt.Fprint(w, body)
			return
		}
		t.Errorf("unexpected page %q", r.URL.Query().Get("page"))
		http.Error(w, "unexpected page", http.StatusBadRequest)
	}))
	defer server.Close()

	client := newGitHubClient(server.URL, "")
	status, err := client.getCombinedStatus(context.Background(), "acme/project", "abc")
	if err != nil {
		t.Fatalf("getCombinedStatus() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("request count = %d, want 2 pages", calls)
	}
	if len(status.Statuses) != 101 || status.Statuses[100].Context != "later" || status.Statuses[100].State != "failure" {
		t.Fatalf("later-page failing status was not preserved: %#v", status.Statuses)
	}
}

func marshalTestJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return string(data)
}
