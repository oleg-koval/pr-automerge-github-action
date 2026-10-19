package action

import "testing"

func TestEventPayloadPullRequestsReturnsEveryCheckSuitePR(t *testing.T) {
	payload := eventPayload{
		CheckSuite: &eventCheckSuite{PullRequests: []eventPullRequest{
			{Number: 3},
			{Number: 8},
		}},
	}

	pullRequests := payload.pullRequests()
	if len(pullRequests) != 2 {
		t.Fatalf("pullRequests count = %d, want 2", len(pullRequests))
	}
	if pullRequests[0].Number != 3 || pullRequests[1].Number != 8 {
		t.Fatalf("pullRequests = %#v, want PRs 3 and 8", pullRequests)
	}
}

func TestEventPayloadPullRequestEventReturnsSinglePR(t *testing.T) {
	payload := eventPayload{PullRequest: &eventPullRequest{Number: 12}}
	pullRequests := payload.pullRequests()
	if len(pullRequests) != 1 || pullRequests[0].Number != 12 {
		t.Fatalf("pullRequests = %#v, want only PR 12", pullRequests)
	}
}
