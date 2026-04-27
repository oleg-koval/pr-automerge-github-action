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
	"strings"
	"time"
)

var errNotFound = errors.New("not found")

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
	State    string         `json:"state"`
	Statuses []commitStatus `json:"statuses"`
}

type commitStatus struct {
	Context string `json:"context"`
	State   string `json:"state"`
}

type checkRunsResponse struct {
	CheckRuns []checkRun `json:"check_runs"`
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
	var status combinedStatus
	err := c.request(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/commits/%s/status", repo, sha), nil, &status)
	return status, err
}

func (c *githubClient) getCheckRuns(ctx context.Context, repo string, sha string) (checkRunsResponse, error) {
	var runs checkRunsResponse
	err := c.request(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/commits/%s/check-runs", repo, sha), nil, &runs)
	return runs, err
}

func (c *githubClient) listComments(ctx context.Context, repo string, number int) ([]issueComment, error) {
	var comments []issueComment
	err := c.request(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/issues/%d/comments", repo, number), nil, &comments)
	return comments, err
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
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
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
