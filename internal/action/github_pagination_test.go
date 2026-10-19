package action

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetCheckRunsPaginatesUntilLastPage(t *testing.T) {
	ok := "success"
	firstPage := make([]checkRun, 10)
	for i := range firstPage {
		firstPage[i] = checkRun{Name: fmt.Sprintf("check-%d", i), Status: "completed", Conclusion: &ok}
	}
	failed := "failure"
	pages := map[string]string{
		"1": marshalTestJSON(t, checkRunsResponse{TotalCount: 11, CheckRuns: firstPage}),
		"2": marshalTestJSON(t, checkRunsResponse{TotalCount: 11, CheckRuns: []checkRun{{Name: "later", Status: "completed", Conclusion: &failed}}}),
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/repos/acme/project/commits/abc/check-runs" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("per_page") != "10" {
			t.Errorf("per_page = %q, want 10", r.URL.Query().Get("per_page"))
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
	if len(runs.CheckRuns) != 11 {
		t.Fatalf("check run count = %d, want 11", len(runs.CheckRuns))
	}
	if runs.CheckRuns[10].Name != "later" || runs.CheckRuns[10].Conclusion == nil || *runs.CheckRuns[10].Conclusion != "failure" {
		t.Fatalf("later-page failing check was not preserved: %#v", runs.CheckRuns[10])
	}
}

func TestGetCombinedStatusPaginatesStatuses(t *testing.T) {
	firstPage := make([]commitStatus, 10)
	for i := range firstPage {
		firstPage[i] = commitStatus{Context: fmt.Sprintf("status-%d", i), State: "success"}
	}
	pages := map[string]string{
		"1": marshalTestJSON(t, combinedStatus{State: "failure", TotalCount: 11, Statuses: firstPage}),
		"2": marshalTestJSON(t, combinedStatus{State: "failure", TotalCount: 11, Statuses: []commitStatus{{Context: "later", State: "failure"}}}),
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/repos/acme/project/commits/abc/status" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("per_page") != "100" {
			t.Errorf("per_page = %q, want 10", r.URL.Query().Get("per_page"))
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
	if len(status.Statuses) != 11 || status.Statuses[10].Context != "later" || status.Statuses[10].State != "failure" {
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

func TestGetCheckRunsRejectsCountChangesBetweenPages(t *testing.T) {
	page := make([]checkRun, 10)
	pages := map[string]checkRunsResponse{
		"1": {TotalCount: 11, CheckRuns: page},
		"2": {TotalCount: 12, CheckRuns: []checkRun{{Name: "later"}}},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(pages[r.URL.Query().Get("page")])
	}))
	defer server.Close()

	_, err := newGitHubClient(server.URL, "").getCheckRuns(context.Background(), "acme/project", "abc")
	if err == nil {
		t.Fatal("getCheckRuns() error = nil, want pagination count-change error")
	}
}

func TestGetCombinedStatusRejectsCountChangesBetweenPages(t *testing.T) {
	page := make([]commitStatus, 10)
	pages := map[string]combinedStatus{
		"1": {TotalCount: 11, Statuses: page},
		"2": {TotalCount: 12, Statuses: []commitStatus{{Context: "later", State: "failure"}}},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(pages[r.URL.Query().Get("page")])
	}))
	defer server.Close()

	_, err := newGitHubClient(server.URL, "").getCombinedStatus(context.Background(), "acme/project", "abc")
	if err == nil {
		t.Fatal("getCombinedStatus() error = nil, want pagination count-change error")
	}
}

func TestGetCheckRunsAcceptsLargeOutputResponse(t *testing.T) {
	output := strings.Repeat("x", 2<<20)
	body := fmt.Sprintf(`{"total_count":1,"check_runs":[{"name":"ci","status":"completed","conclusion":"success","output":{"summary":%q}}]}`, output)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, body)
	}))
	defer server.Close()

	runs, err := newGitHubClient(server.URL, "").getCheckRuns(context.Background(), "acme/project", "abc")
	if err != nil {
		t.Fatalf("getCheckRuns() error = %v, want large valid response to decode", err)
	}
	if len(runs.CheckRuns) != 1 || runs.CheckRuns[0].Name != "ci" {
		t.Fatal("getCheckRuns() did not parse the check run with large output")
	}
}
