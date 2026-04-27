package action

import (
	"encoding/json"
	"fmt"
	"os"
)

type eventPayload struct {
	PullRequest *eventPullRequest `json:"pull_request"`
	CheckSuite  *eventCheckSuite  `json:"check_suite"`
	Repository  eventRepository   `json:"repository"`
}

func (p eventPayload) pullRequest() *eventPullRequest {
	if p.PullRequest != nil {
		return p.PullRequest
	}
	if p.CheckSuite != nil && len(p.CheckSuite.PullRequests) > 0 {
		return &p.CheckSuite.PullRequests[0]
	}
	return nil
}

type eventPullRequest struct {
	Number int       `json:"number"`
	Draft  bool      `json:"draft"`
	User   eventUser `json:"user"`
	Head   eventRef  `json:"head"`
}

type eventCheckSuite struct {
	PullRequests []eventPullRequest `json:"pull_requests"`
}

type eventRepository struct {
	FullName string `json:"full_name"`
}

type eventUser struct {
	Login string `json:"login"`
}

type eventRef struct {
	SHA string `json:"sha"`
}

func loadEvent(path string) (eventPayload, error) {
	if path == "" {
		return eventPayload{}, fmt.Errorf("GITHUB_EVENT_PATH is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return eventPayload{}, fmt.Errorf("read event payload: %w", err)
	}
	var payload eventPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return eventPayload{}, fmt.Errorf("parse event payload: %w", err)
	}
	return payload, nil
}
