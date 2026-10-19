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
	"strconv"
	"strings"
	"time"
)

var errNotFound = errors.New("not found")

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
	Draft          bool   `json:"draft"`
	Mergeable      *bool  `json:"mergeable"`
	MergeableState string `json:"mergeable_state"`
	User           ghUser `json:"user"`
	Head           ghHead `json:"head"`
}

type ghUser struct {
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
	Context string `json:"context"`
	State   string `json:"state"`
}

type checkRunsResponse struct {
	TotalCount int        `json:"total_count"`
	CheckRuns  []checkRun `json:"check_runs"`
}

type checkRun struct {
	Name       string  `json:"name"`
	Status     string  `json:"status"`
	Conclusion *string `json:"conclusion"`
	DetailsURL string  `json:"details_url"`
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
	var combined combinedStatus
	for page := 1; ; page++ {
		var current combinedStatus
		path := fmt.Sprintf("/repos/%s/commits/%s/status?per_page=100&page=%d", repo, sha, page)
		if err := c.request(ctx, http.MethodGet, path, nil, &current); err != nil {
			return combinedStatus{}, err
		}
		if page == 1 {
			combined.State = current.State
			combined.TotalCount = current.TotalCount
		}
		combined.Statuses = append(combined.Statuses, current.Statuses...)
		if len(current.Statuses) == 0 || len(combined.Statuses) >= combined.TotalCount {
			return combined, nil
		}
	}
}

func (c *githubClient) getCheckRuns(ctx context.Context, repo string, sha string) (checkRunsResponse, error) {
	var combined checkRunsResponse
	for page := 1; ; page++ {
		var current checkRunsResponse
		path := fmt.Sprintf("/repos/%s/commits/%s/check-runs?per_page=100&page=%d", repo, sha, page)
		if err := c.request(ctx, http.MethodGet, path, nil, &current); err != nil {
			return checkRunsResponse{}, err
		}
		if page == 1 {
			combined.TotalCount = current.TotalCount
		}
		combined.CheckRuns = append(combined.CheckRuns, current.CheckRuns...)
		if len(current.CheckRuns) == 0 || len(combined.CheckRuns) >= combined.TotalCount {
			return combined, nil
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

func (c *githubClient) createComment(ctx context.Context, repo string, number int, body string) error {
	payload := map[string]string{"body": body}
	return c.request(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/issues/%d/comments", repo, number), payload, nil)
}

func (c *githubClient) mergePullRequest(ctx context.Context, repo string, number int, method string) error {
	payload := map[string]string{"merge_method": method}
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
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
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
		return fmt.Errorf("github api %s %s returned %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if target == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode github response: %w", err)
	}
	return nil
}
