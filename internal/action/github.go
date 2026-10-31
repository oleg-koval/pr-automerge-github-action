package action

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
)

var (
	errNotFound          = errors.New("not found")
	errPaginationChanged = errors.New("results changed during pagination")
)

const (
	statusPageSize        = 100
	checkRunPageSize      = 100
	maxGitHubResponseSize = 32 << 20
)

type githubRateLimitError struct {
	resetAt time.Time
	message string
}

func (e *githubRateLimitError) Error() string {
	if e.message == "" {
		return "github api rate limit exceeded"
	}
	return "github api rate limit exceeded: " + e.message
}

type githubClient struct {
	baseURL string
	token   string
	client  *http.Client
}

type githubAPIError struct {
	method     string
	path       string
	statusCode int
	body       string
}

func (e *githubAPIError) Error() string {
	return fmt.Sprintf("github api %s %s returned %d: %s", e.method, e.path, e.statusCode, e.body)
}

func newGitHubClient(baseURL string, token string) *githubClient {
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	return &githubClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		client:  &http.Client{Timeout: 20 * time.Second},
	}
}

type pullRequest struct {
	Number         int    `json:"number"`
	State          string `json:"state"`
	Merged         bool   `json:"merged"`
	Draft          bool   `json:"draft"`
	Mergeable      *bool  `json:"mergeable"`
	MergeableState string `json:"mergeable_state"`
	User           ghUser `json:"user"`
	Head           ghHead `json:"head"`
}

type ghUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

type ghHead struct {
	SHA string `json:"sha"`
}

type contentResponse struct {
	Content string `json:"content"`
}

type combinedStatus struct {
	State      string         `json:"state"`
	TotalCount int            `json:"total_count"`
	Statuses   []commitStatus `json:"statuses"`
}

type commitStatus struct {
	ID        int64     `json:"id"`
	Context   string    `json:"context"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	Creator   ghUser    `json:"creator"`
}

type checkRunsResponse struct {
	TotalCount int        `json:"total_count"`
	CheckRuns  []checkRun `json:"check_runs"`
}

type checkRun struct {
	ID         int64         `json:"id"`
	Name       string        `json:"name"`
	Status     string        `json:"status"`
	Conclusion *string       `json:"conclusion"`
	DetailsURL string        `json:"details_url"`
	App        *ghApp        `json:"app"`
	CheckSuite *ghCheckSuite `json:"check_suite"`
}

type ghCheckSuite struct {
	ID int64 `json:"id"`
}

type workflowRun struct {
	ID           int64  `json:"id"`
	Path         string `json:"path"`
	HeadSHA      string `json:"head_sha"`
	CheckSuiteID int64  `json:"check_suite_id"`
	Repository   struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

func (c *githubClient) getWorkflowRun(ctx context.Context, repo string, id int64) (workflowRun, error) {
	var run workflowRun
	err := c.request(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/actions/runs/%d", repo, id), nil, &run)
	return run, err
}

type ghApp struct {
	ID int64 `json:"id"`
}

type issueComment struct {
	Body string `json:"body"`
}

type reviewThreadsResponse struct {
	Data struct {
		Repository *reviewRepository `json:"repository"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

type prCommentsResponse struct {
	Data struct {
		Repository *struct {
			PullRequest *struct {
				Comments      *commentCountConnection `json:"comments"`
				ReviewThreads *reviewThreadConnection `json:"reviewThreads"`
				Reviews       *reviewBodyConnection   `json:"reviews"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

type commentCountConnection struct {
	TotalCount int `json:"totalCount"`
}

type reviewBodyConnection struct {
	Nodes    []reviewBody `json:"nodes"`
	PageInfo *pageInfo    `json:"pageInfo"`
}

type reviewBody struct {
	Body string `json:"body"`
}

type reviewRepository struct {
	PullRequest *reviewPullRequest `json:"pullRequest"`
}

type reviewPullRequest struct {
	ReviewThreads *reviewThreadConnection `json:"reviewThreads"`
}

type reviewThreadConnection struct {
	Nodes    []reviewThread `json:"nodes"`
	PageInfo *pageInfo      `json:"pageInfo"`
}

type reviewThread struct {
	IsResolved bool `json:"isResolved"`
}

type pageInfo struct {
	HasNextPage bool    `json:"hasNextPage"`
	EndCursor   *string `json:"endCursor"`
}

func (c *githubClient) graphqlURL() string {
	base := strings.TrimRight(c.baseURL, "/")
	if strings.HasSuffix(base, "/api/v3") {
		return strings.TrimSuffix(base, "/api/v3") + "/api/graphql"
	}
	return base + "/graphql"
}

func (c *githubClient) getPullRequest(ctx context.Context, repo string, number int) (pullRequest, error) {
	var pr pullRequest
	err := c.request(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d", repo, number), nil, &pr)
	return pr, err
}

func (c *githubClient) getPullRequestWithMergeability(ctx context.Context, repo string, number int) (pullRequest, error) {
	var pr pullRequest
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		pr, err = c.getPullRequest(ctx, repo, number)
		if err != nil {
			return pullRequest{}, err
		}
		if pr.Mergeable != nil {
			return pr, nil
		}
		time.Sleep(time.Duration(attempt+1) * time.Second)
	}
	return pr, nil
}

func (c *githubClient) getContent(ctx context.Context, repo string, path string) (contentResponse, error) {
	var content contentResponse
	err := c.request(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/contents/%s", repo, escapePath(path)), nil, &content)
	return content, err
}

func escapePath(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func (c *githubClient) getCombinedStatus(ctx context.Context, repo string, sha string) (combinedStatus, error) {
	first, paginated, err := c.scanCombinedStatus(ctx, repo, sha)
	if err != nil || !paginated {
		return first, err
	}
	second, _, err := c.scanCombinedStatus(ctx, repo, sha)
	if err != nil {
		return combinedStatus{}, err
	}
	if !reflect.DeepEqual(first, second) {
		return combinedStatus{}, errPaginationChanged
	}
	return second, nil
}

func (c *githubClient) scanCombinedStatus(ctx context.Context, repo string, sha string) (combinedStatus, bool, error) {
	var combined combinedStatus
	seen := make(map[int64]bool)
	for page := 1; ; page++ {
		var current combinedStatus
		path := fmt.Sprintf("/repos/%s/commits/%s/status?per_page=%d&page=%d", repo, sha, statusPageSize, page)
		if err := c.request(ctx, http.MethodGet, path, nil, &current); err != nil {
			if page > 1 && errors.Is(err, errNotFound) {
				return combinedStatus{}, false, fmt.Errorf("commit status page %d missing: %w", page, &githubAPIError{method: http.MethodGet, path: path, statusCode: http.StatusNotFound})
			}
			return combinedStatus{}, false, err
		}
		if page == 1 {
			combined.State = current.State
			combined.TotalCount = current.TotalCount
		} else if current.TotalCount != combined.TotalCount {
			return combinedStatus{}, false, fmt.Errorf("%w: commit statuses total_count was %d and is now %d", errPaginationChanged, combined.TotalCount, current.TotalCount)
		}
		for _, record := range current.Statuses {
			if record.ID != 0 {
				if seen[record.ID] {
					return combinedStatus{}, false, errPaginationChanged
				}
				seen[record.ID] = true
			}
		}
		combined.Statuses = append(combined.Statuses, current.Statuses...)
		if len(combined.Statuses) > combined.TotalCount {
			return combinedStatus{}, false, fmt.Errorf("commit status pagination returned %d records for total_count %d", len(combined.Statuses), combined.TotalCount)
		}
		if len(combined.Statuses) == combined.TotalCount {
			return combined, page > 1, nil
		}
		if len(current.Statuses) == 0 {
			return combinedStatus{}, false, fmt.Errorf("commit status pagination stopped at %d of %d records", len(combined.Statuses), combined.TotalCount)
		}
	}
}

func (c *githubClient) getCheckRuns(ctx context.Context, repo string, sha string) (checkRunsResponse, error) {
	first, paginated, err := c.scanCheckRuns(ctx, repo, sha)
	if err != nil || !paginated {
		return first, err
	}
	second, _, err := c.scanCheckRuns(ctx, repo, sha)
	if err != nil {
		return checkRunsResponse{}, err
	}
	if !reflect.DeepEqual(first, second) {
		return checkRunsResponse{}, errPaginationChanged
	}
	return second, nil
}

func (c *githubClient) scanCheckRuns(ctx context.Context, repo string, sha string) (checkRunsResponse, bool, error) {
	var combined checkRunsResponse
	seen := make(map[int64]bool)
	for page := 1; ; page++ {
		var current checkRunsResponse
		path := fmt.Sprintf("/repos/%s/commits/%s/check-runs?per_page=%d&page=%d", repo, sha, checkRunPageSize, page)
		if err := c.request(ctx, http.MethodGet, path, nil, &current); err != nil {
			if page > 1 && errors.Is(err, errNotFound) {
				return checkRunsResponse{}, false, fmt.Errorf("check run page %d missing: %w", page, &githubAPIError{method: http.MethodGet, path: path, statusCode: http.StatusNotFound})
			}
			return checkRunsResponse{}, false, err
		}
		if page == 1 {
			combined.TotalCount = current.TotalCount
		} else if current.TotalCount != combined.TotalCount {
			return checkRunsResponse{}, false, fmt.Errorf("%w: check runs total_count was %d and is now %d", errPaginationChanged, combined.TotalCount, current.TotalCount)
		}
		for _, record := range current.CheckRuns {
			if record.ID != 0 {
				if seen[record.ID] {
					return checkRunsResponse{}, false, errPaginationChanged
				}
				seen[record.ID] = true
			}
		}
		combined.CheckRuns = append(combined.CheckRuns, current.CheckRuns...)
		if len(combined.CheckRuns) > combined.TotalCount {
			return checkRunsResponse{}, false, fmt.Errorf("check run pagination returned %d records for total_count %d", len(combined.CheckRuns), combined.TotalCount)
		}
		if len(combined.CheckRuns) == combined.TotalCount {
			return combined, page > 1, nil
		}
		if len(current.CheckRuns) == 0 {
			return checkRunsResponse{}, false, fmt.Errorf("check run pagination stopped at %d of %d records", len(combined.CheckRuns), combined.TotalCount)
		}
	}
}

func (c *githubClient) listComments(ctx context.Context, repo string, number int) ([]issueComment, error) {
	var comments []issueComment
	err := c.request(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/issues/%d/comments", repo, number), nil, &comments)
	return comments, err
}

func (c *githubClient) hasOpenReviewComments(ctx context.Context, repo string, number int) (bool, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return false, fmt.Errorf("repository must be owner/name, got %q", repo)
	}

	const query = `query($owner:String!,$name:String!,$number:Int!,$after:String){repository(owner:$owner,name:$name){pullRequest(number:$number){reviewThreads(first:100,after:$after){nodes{isResolved}pageInfo{hasNextPage,endCursor}}}}}`
	var cursor *string
	for {
		variables := map[string]any{
			"owner":  owner,
			"name":   name,
			"number": number,
			"after":  cursor,
		}
		var response reviewThreadsResponse
		if err := c.request(ctx, http.MethodPost, c.graphqlURL(), map[string]any{"query": query, "variables": variables}, &response); err != nil {
			return false, err
		}
		if len(response.Errors) > 0 {
			message := response.Errors[0].Message
			if strings.Contains(strings.ToLower(message), "rate limit") {
				return false, &githubRateLimitError{message: message}
			}
			return false, fmt.Errorf("github graphql: %s", message)
		}
		if response.Data.Repository == nil || response.Data.Repository.PullRequest == nil || response.Data.Repository.PullRequest.ReviewThreads == nil {
			return false, errors.New("github graphql: reviewThreads connection missing")
		}
		connection := response.Data.Repository.PullRequest.ReviewThreads
		if connection.PageInfo == nil {
			return false, errors.New("github graphql: reviewThreads pageInfo missing")
		}
		for _, thread := range connection.Nodes {
			if !thread.IsResolved {
				return true, nil
			}
		}
		page := connection.PageInfo
		if !page.HasNextPage {
			return false, nil
		}
		if page.EndCursor == nil || *page.EndCursor == "" {
			return false, errors.New("github graphql: reviewThreads pageInfo hasNextPage without endCursor")
		}
		cursor = page.EndCursor
	}
}

func (c *githubClient) hasPRComments(ctx context.Context, repo string, number int) (bool, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return false, fmt.Errorf("repository must be owner/name, got %q", repo)
	}

	const query = `query($owner:String!,$name:String!,$number:Int!,$threadAfter:String,$reviewAfter:String){repository(owner:$owner,name:$name){pullRequest(number:$number){comments(first:1){totalCount} reviewThreads(first:100,after:$threadAfter){nodes{isResolved}pageInfo{hasNextPage,endCursor}} reviews(first:100,after:$reviewAfter){nodes{body}pageInfo{hasNextPage,endCursor}}}}}`
	var threadCursor, reviewCursor *string
	confirmedEmpty := false
	for {
		variables := map[string]any{
			"owner": owner, "name": name, "number": number,
			"threadAfter": threadCursor, "reviewAfter": reviewCursor,
		}
		var response prCommentsResponse
		if err := c.request(ctx, http.MethodPost, c.graphqlURL(), map[string]any{"query": query, "variables": variables}, &response); err != nil {
			return false, err
		}
		if len(response.Errors) > 0 {
			message := response.Errors[0].Message
			if strings.Contains(strings.ToLower(message), "rate limit") {
				return false, &githubRateLimitError{message: message}
			}
			return false, fmt.Errorf("github graphql: %s", message)
		}
		if response.Data.Repository == nil || response.Data.Repository.PullRequest == nil {
			return false, errors.New("github graphql: pull request not found")
		}
		pr := response.Data.Repository.PullRequest
		if pr.Comments == nil || pr.ReviewThreads == nil || pr.ReviewThreads.PageInfo == nil || pr.Reviews == nil || pr.Reviews.PageInfo == nil {
			return false, errors.New("github graphql: PR comments connection missing")
		}
		if pr.Comments.TotalCount > 0 || len(pr.ReviewThreads.Nodes) > 0 {
			return true, nil
		}
		for _, review := range pr.Reviews.Nodes {
			if strings.TrimSpace(review.Body) != "" {
				return true, nil
			}
		}
		threadPage := pr.ReviewThreads.PageInfo
		reviewPage := pr.Reviews.PageInfo
		if !threadPage.HasNextPage && !reviewPage.HasNextPage {
			if confirmedEmpty {
				return false, nil
			}
			// Re-read every page to catch comments added to already-fetched pages.
			confirmedEmpty = true
			threadCursor, reviewCursor = nil, nil
			continue
		}
		if threadPage.HasNextPage {
			if threadPage.EndCursor == nil || *threadPage.EndCursor == "" {
				return false, errors.New("github graphql: reviewThreads pageInfo hasNextPage without endCursor")
			}
			threadCursor = threadPage.EndCursor
		}
		if reviewPage.HasNextPage {
			if reviewPage.EndCursor == nil || *reviewPage.EndCursor == "" {
				return false, errors.New("github graphql: reviews pageInfo hasNextPage without endCursor")
			}
			reviewCursor = reviewPage.EndCursor
		}
	}
}

func (c *githubClient) createComment(ctx context.Context, repo string, number int, body string) error {
	payload := map[string]string{"body": body}
	return c.request(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/issues/%d/comments", repo, number), payload, nil)
}

func (c *githubClient) mergePullRequest(ctx context.Context, repo string, number int, method string, expectedHeadSHA string) error {
	payload := map[string]string{"merge_method": method, "sha": expectedHeadSHA}
	return c.request(ctx, http.MethodPut, fmt.Sprintf("/repos/%s/pulls/%d/merge", repo, number), payload, nil)
}

func (c *githubClient) updateBranch(ctx context.Context, repo string, number int) error {
	return c.request(ctx, http.MethodPut, fmt.Sprintf("/repos/%s/pulls/%d/update-branch", repo, number), map[string]string{}, nil)
}

func (c *githubClient) request(ctx context.Context, method string, path string, payload any, target any) error {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	endpoint := c.baseURL + path
	if strings.HasPrefix(path, "https://") || strings.HasPrefix(path, "http://") {
		endpoint = path
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxGitHubResponseSize+1))
	if err != nil {
		return err
	}
	if len(data) > maxGitHubResponseSize {
		return fmt.Errorf("github api response exceeded %d bytes", maxGitHubResponseSize)
	}
	if resp.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	if resp.StatusCode == http.StatusTooManyRequests || (resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0") {
		resetAt := time.Time{}
		if resetUnix, parseErr := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); parseErr == nil {
			resetAt = time.Unix(resetUnix, 0)
		} else if retryAfter, parseErr := strconv.ParseInt(resp.Header.Get("Retry-After"), 10, 64); parseErr == nil {
			resetAt = time.Now().Add(time.Duration(retryAfter) * time.Second)
		}
		return &githubRateLimitError{resetAt: resetAt, message: strings.TrimSpace(string(data))}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &githubAPIError{method: method, path: path, statusCode: resp.StatusCode, body: strings.TrimSpace(string(data))}
	}
	if target == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode github response: %w", err)
	}
	return nil
}
