package action

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const reviewerHead = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestCurrentHeadReviewerCompletion(t *testing.T) {
	for _, tc := range []struct {
		name     string
		login    string
		userType string
		state    string
		body     string
		sha      string
		status   int
		want     bool
		wantErr  bool
	}{
		{name: "Codex approval", login: "codex-review[bot]", userType: "Bot", state: "APPROVED", sha: reviewerHead, want: true},
		{name: "Codex completed review with findings in body", login: "codex-review[bot]", userType: "Bot", state: "COMMENTED", body: "Critical issue found; this review is complete.", sha: reviewerHead, want: true},
		{name: "provider review quota message", login: "codex-review[bot]", userType: "Bot", state: "COMMENTED", body: "You've hit your code review limit", sha: reviewerHead},
		{name: "provider usage limit message", login: "codex-review[bot]", userType: "Bot", state: "COMMENTED", body: "Usage limit reached", sha: reviewerHead},
		{name: "provider quota message", login: "codex-review[bot]", userType: "Bot", state: "COMMENTED", body: "Review quota exceeded", sha: reviewerHead},
		{name: "provider unable to review message", login: "codex-review[bot]", userType: "Bot", state: "COMMENTED", body: "Unable to review this pull request", sha: reviewerHead},
		{name: "provider review unavailable message", login: "codex-review[bot]", userType: "Bot", state: "COMMENTED", body: "Review unavailable", sha: reviewerHead},
		{name: "provider review skipped message", login: "codex-review[bot]", userType: "Bot", state: "COMMENTED", body: "Review skipped", sha: reviewerHead},
		{name: "approval with skipped body", login: "codex-review[bot]", userType: "Bot", state: "APPROVED", body: "Review skipped", sha: reviewerHead},
		{name: "ordinary quota finding", login: "codex-review[bot]", userType: "Bot", state: "COMMENTED", body: "Fix quota enforcement for this endpoint.", sha: reviewerHead, want: true},
		{name: "ordinary usage limit finding", login: "codex-review[bot]", userType: "Bot", state: "APPROVED", body: "The usage limit should be configurable.", sha: reviewerHead, want: true},
		{name: "missing reviewer", login: "other[bot]", userType: "Bot", state: "APPROVED", sha: reviewerHead},
		{name: "stale head", login: "codex-review[bot]", userType: "Bot", state: "APPROVED", sha: strings.Repeat("b", 40)},
		{name: "spoofed account type", login: "codex-review[bot]", userType: "User", state: "APPROVED", sha: reviewerHead},
		{name: "changes requested", login: "codex-review[bot]", userType: "Bot", state: "CHANGES_REQUESTED", sha: reviewerHead},
		{name: "dismissed", login: "codex-review[bot]", userType: "Bot", state: "DISMISSED", sha: reviewerHead},
		{name: "review API unavailable", login: "codex-review[bot]", userType: "Bot", state: "APPROVED", sha: reviewerHead, status: http.StatusForbidden, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/repos/acme/project/pulls/42/reviews" {
					http.NotFound(w, r)
					return
				}
				if tc.status != 0 {
					http.Error(w, "API rate limit exceeded", tc.status)
					return
				}
				_ = json.NewEncoder(w).Encode([]pullRequestReview{{ID: 7, User: reviewUser{Login: tc.login, Type: tc.userType}, State: tc.state, Body: tc.body, CommitID: tc.sha, SubmittedAt: timePointer(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))}})
			}))
			defer server.Close()

			got, err := newGitHubClient(server.URL, "").hasCurrentHeadReviewerCompletion(context.Background(), "acme/project", 42, "codex-review[bot]", reviewerHead)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("completed = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLatestReviewerStateWins(t *testing.T) {
	created := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]pullRequestReview{
			{ID: 1, User: reviewUser{Login: "codex-review[bot]", Type: "Bot"}, State: "APPROVED", CommitID: reviewerHead, SubmittedAt: timePointer(created)},
			{ID: 2, User: reviewUser{Login: "codex-review[bot]", Type: "Bot"}, State: "CHANGES_REQUESTED", CommitID: reviewerHead, SubmittedAt: timePointer(created.Add(time.Minute))},
		})
	}))
	defer server.Close()

	got, err := newGitHubClient(server.URL, "").hasCurrentHeadReviewerCompletion(context.Background(), "acme/project", 42, "codex-review[bot]", reviewerHead)
	if err != nil || got {
		t.Fatalf("completion = %v, error = %v; latest changes-requested review must block", got, err)
	}
}

func TestSuppressCommentsDoesNotRequireAnEmptyDiscussion(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()

	client := newGitHubClient(server.URL, "")
	cfg := Config{SuppressComments: true}
	if err := postCommentOnce(context.Background(), client, cfg, "acme/project", 42, "action notice", log.New(io.Discard, "", 0)); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 0 {
		t.Fatalf("suppressed action comment made %d API requests", requests.Load())
	}
}

func TestProcessPullRequestRequiresLatestCurrentHeadReviewAndSuppressesComments(t *testing.T) {
	for _, tc := range []struct {
		name          string
		review        string
		apiFailure    bool
		failureBody   string
		headChange    bool
		threadOpens   bool
		noPRComments  bool
		multiReviewer bool
		discussion    bool
		wantMerge     bool
		wantErr       bool
	}{
		{name: "completed Codex review comment body is allowed", review: "COMMENTED", wantMerge: true},
		{name: "multiple reviewers share one snapshot", review: "COMMENTED", multiReviewer: true, wantMerge: true},
		{name: "no-pr-comments allows empty discussion", review: "COMMENTED", noPRComments: true, wantMerge: true},
		{name: "no review", review: "missing"},
		{name: "stale review", review: "stale"},
		{name: "spoofed reviewer", review: "spoofed"},
		{name: "latest review changes requested", review: "CHANGES_REQUESTED"},
		{name: "provider failure fails closed", review: "APPROVED", apiFailure: true, wantErr: true},
		{name: "provider failure body fails closed", review: "COMMENTED", failureBody: "You have hit your code review limit"},
		{name: "head change before merge", review: "COMMENTED", headChange: true},
		{name: "thread opened during reviewer lookup", review: "COMMENTED", threadOpens: true},
		{name: "no-pr-comments still requires an empty discussion", review: "COMMENTED", noPRComments: true, discussion: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var pullReads, merges, comments atomic.Int32
			var reviewRead atomic.Bool
			var reviewReads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/repos/acme/project/pulls/42" && r.Method == http.MethodGet:
					read := pullReads.Add(1)
					head := reviewerHead
					if tc.headChange && read > 1 {
						head = strings.Repeat("b", 40)
					}
					_, _ = fmt.Fprintf(w, `{"number":42,"state":"open","draft":false,"mergeable":true,"mergeable_state":"clean","user":{"login":"renovate[bot]"},"head":{"sha":%q}}`, head)
				case strings.HasPrefix(r.URL.Path, "/repos/acme/project/commits/") && strings.HasSuffix(r.URL.Path, "/status"):
					_, _ = fmt.Fprint(w, `{"state":"success","total_count":1,"statuses":[{"id":1,"context":"ci","state":"success"}]}`)
				case strings.HasPrefix(r.URL.Path, "/repos/acme/project/commits/") && strings.HasSuffix(r.URL.Path, "/check-runs"):
					_, _ = fmt.Fprint(w, `{"total_count":0,"check_runs":[]}`)
				case r.URL.Path == "/graphql":
					if tc.noPRComments {
						count := 0
						if tc.discussion {
							count = 1
						}
						_, _ = fmt.Fprintf(w, `{"data":{"repository":{"pullRequest":{"comments":{"totalCount":%d},"reviewThreads":{"nodes":[],"pageInfo":{"hasNextPage":false}},"reviews":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}}}`, count)
					} else {
						thread := `[]`
						if tc.threadOpens && reviewRead.Load() {
							thread = `[{"isResolved":false}]`
						}
						_, _ = fmt.Fprintf(w, `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":%s,"pageInfo":{"hasNextPage":false}}}}}}`, thread)
					}
				case r.URL.Path == "/repos/acme/project/pulls/42/reviews":
					reviewRead.Store(true)
					reviewReads.Add(1)
					if tc.apiFailure {
						http.Error(w, "API rate limit exceeded", http.StatusForbidden)
						return
					}
					login, userType, state, sha := "codex-review[bot]", "Bot", tc.review, reviewerHead
					switch tc.review {
					case "missing":
						_, _ = fmt.Fprint(w, `[]`)
						return
					case "stale":
						sha = strings.Repeat("b", 40)
					case "spoofed":
						login = "other[bot]"
					}
					body := "Critical issue found; review complete."
					if tc.failureBody != "" {
						body = tc.failureBody
					}
					reviews := []pullRequestReview{{ID: 1, User: reviewUser{Login: login, Type: userType}, State: state, Body: body, CommitID: sha, SubmittedAt: timePointer(time.Now())}}
					if tc.multiReviewer {
						other := reviews[0]
						other.ID = 2
						other.User.Login = "security-review[bot]"
						reviews = append(reviews, other)
					}
					_ = json.NewEncoder(w).Encode(reviews)
				case r.URL.Path == "/repos/acme/project/pulls/42/merge" && r.Method == http.MethodPut:
					merges.Add(1)
					_, _ = fmt.Fprint(w, `{"merged":true}`)
				case r.URL.Path == "/repos/acme/project/issues/42/comments" && r.Method == http.MethodGet:
					_, _ = fmt.Fprint(w, `[]`)
				case r.URL.Path == "/repos/acme/project/issues/42/comments" && r.Method == http.MethodPost:
					comments.Add(1)
					_, _ = fmt.Fprint(w, `{"id":1}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			client := newGitHubClient(server.URL, "")
			cfg := Config{
				Bots: []string{"renovate[bot]"}, Maintainers: []string{"maintainer"}, MergeMethod: "squash",
				WaitTimeout: 0, WaitInterval: time.Second, SuppressComments: true, NoPRComments: tc.noPRComments,
				RequiredReviewerLogins: []string{"codex-review[bot]"}, ExpectedHeadSHA: reviewerHead,
			}
			if tc.multiReviewer {
				cfg.RequiredReviewerLogins = append(cfg.RequiredReviewerLogins, "security-review[bot]")
			}
			err := processPullRequest(context.Background(), env{}, client, cfg, "acme/project", eventPullRequest{Number: 42}, log.New(io.Discard, "", 0))
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if got := merges.Load() == 1; got != tc.wantMerge {
				t.Fatalf("merged = %v, want %v", got, tc.wantMerge)
			}
			if tc.multiReviewer && reviewReads.Load() != 1 {
				t.Fatalf("review list fetched %d times, want one shared snapshot", reviewReads.Load())
			}
			if comments.Load() != 0 {
				t.Fatalf("suppression allowed %d action-generated comments", comments.Load())
			}
		})
	}
}

func timePointer(value time.Time) *time.Time { return &value }

func TestReviewerCompletionPaginatesAndChecksConsistency(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprintf("changed=%t", changed), func(t *testing.T) {
			created := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := calls.Add(1)
				var reviews []pullRequestReview
				switch r.URL.Query().Get("page") {
				case "1":
					for id := int64(1); id <= 100; id++ {
						reviews = append(reviews, pullRequestReview{ID: id, User: reviewUser{Login: "codex-review[bot]", Type: "Bot"}, State: "CHANGES_REQUESTED", CommitID: reviewerHead, SubmittedAt: timePointer(created)})
					}
				case "2":
					state := "APPROVED"
					if changed && call > 2 {
						state = "CHANGES_REQUESTED"
					}
					reviews = []pullRequestReview{{ID: 101, User: reviewUser{Login: "codex-review[bot]", Type: "Bot"}, State: state, CommitID: reviewerHead, SubmittedAt: timePointer(created.Add(time.Minute))}}
				default:
					t.Errorf("unexpected page %q", r.URL.Query().Get("page"))
				}
				_ = json.NewEncoder(w).Encode(reviews)
			}))
			defer server.Close()
			got, err := newGitHubClient(server.URL, "").hasCurrentHeadReviewerCompletion(context.Background(), "acme/project", 42, "codex-review[bot]", reviewerHead)
			if changed {
				if !errors.Is(err, errPaginationChanged) || got {
					t.Fatalf("completed=%t error=%v, want pagination change", got, err)
				}
			} else if err != nil || !got {
				t.Fatalf("completed=%t error=%v, want latest page-two approval", got, err)
			}
			if calls.Load() != 4 {
				t.Fatalf("requests=%d, want two complete two-page scans", calls.Load())
			}
		})
	}
}
