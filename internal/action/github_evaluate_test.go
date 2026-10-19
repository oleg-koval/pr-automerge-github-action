package action

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
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
