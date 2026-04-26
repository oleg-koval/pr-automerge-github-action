package action

import (
	"context"
	"fmt"
	"log"
	"strings"
)

type checkState string

const (
	checksPassed  checkState = "passed"
	checksPending checkState = "pending"
	checksFailed  checkState = "failed"
)

func Run(ctx context.Context, environ []string, logger *log.Logger) error {
	env := newEnv(environ)
	eventName := env.get("GITHUB_EVENT_NAME")
	if eventName != "pull_request" && eventName != "pull_request_target" {
		logger.Printf("not a PR event: %s", eventName)
		return nil
	}

	payload, err := loadEvent(env.get("GITHUB_EVENT_PATH"))
	if err != nil {
		return err
	}
	if payload.PullRequest == nil {
		logger.Printf("event has no pull_request payload")
		return nil
	}

	repo := valueOr(payload.Repository.FullName, env.get("GITHUB_REPOSITORY"))
	if repo == "" {
		return fmt.Errorf("repository full name is required")
	}

	token := valueOr(env.input("github-token"), env.get("GITHUB_TOKEN"))
	gh := newGitHubClient(env.get("GITHUB_API_URL"), token)
	cfg, err := loadConfig(ctx, gh, env, repo)
	if err != nil {
		return err
	}

	pr, err := gh.getPullRequestWithMergeability(ctx, repo, payload.PullRequest.Number)
	if err != nil {
		return err
	}
	if pr.Number == 0 {
		pr.Number = payload.PullRequest.Number
	}
	if pr.User.Login == "" {
		pr.User.Login = payload.PullRequest.User.Login
	}
	if pr.Head.SHA == "" {
		pr.Head.SHA = payload.PullRequest.Head.SHA
	}
	if pr.Draft || payload.PullRequest.Draft {
		logger.Printf("skip draft PR #%d", pr.Number)
		return nil
	}
	if !containsLogin(cfg.Bots, pr.User.Login) {
		logger.Printf("skip PR #%d from non-configured author %s", pr.Number, pr.User.Login)
		return nil
	}

	state, err := evaluateChecks(ctx, gh, repo, pr.Head.SHA)
	if err != nil {
		return err
	}
	if state == checksPending {
		logger.Printf("checks pending for PR #%d", pr.Number)
		return nil
	}
	if state == checksFailed {
		body := maintainerComment(cfg, pr, "failed-checks", "Checks failed for this maintenance bot PR. This may be a breaking dependency or security update and needs maintainer review.")
		return postCommentOnce(ctx, gh, cfg, repo, pr.Number, body, logger)
	}

	if hasMergeConflict(pr) {
		if pr.User.Login == "dependabot[bot]" {
			body := markerComment(pr, "dependabot-conflict", cfg.DependabotRebaseComment)
			return postCommentOnce(ctx, gh, cfg, repo, pr.Number, body, logger)
		}
		body := maintainerComment(cfg, pr, "merge-conflict", "This maintenance bot PR has a merge conflict and needs manual rebase or conflict resolution.")
		return postCommentOnce(ctx, gh, cfg, repo, pr.Number, body, logger)
	}

	if pr.Mergeable == nil {
		body := maintainerComment(cfg, pr, "unknown-mergeability", "GitHub did not report whether this maintenance bot PR is mergeable. Please review it manually.")
		return postCommentOnce(ctx, gh, cfg, repo, pr.Number, body, logger)
	}

	if cfg.DryRun {
		logger.Printf("dry run: would merge PR #%d with method %s", pr.Number, cfg.MergeMethod)
		return nil
	}
	if err := gh.mergePullRequest(ctx, repo, pr.Number, cfg.MergeMethod); err != nil {
		body := maintainerComment(cfg, pr, "merge-failed", "GitHub refused to merge this maintenance bot PR. Please review it manually.\n\nError: "+err.Error())
		return postCommentOnce(ctx, gh, cfg, repo, pr.Number, body, logger)
	}
	logger.Printf("merged PR #%d with method %s", pr.Number, cfg.MergeMethod)
	return nil
}

func evaluateChecks(ctx context.Context, gh *githubClient, repo string, sha string) (checkState, error) {
	status, err := gh.getCombinedStatus(ctx, repo, sha)
	if err != nil && !isOptionalAPIError(err) {
		return checksFailed, err
	}
	runs, err := gh.getCheckRuns(ctx, repo, sha)
	if err != nil && !isOptionalAPIError(err) {
		return checksFailed, err
	}
	if status.State == "pending" {
		return checksPending, nil
	}
	if status.State == "failure" || status.State == "error" {
		return checksFailed, nil
	}
	for _, run := range runs.CheckRuns {
		if run.Status != "completed" {
			return checksPending, nil
		}
		if run.Conclusion == nil {
			return checksPending, nil
		}
		if !allowedConclusion(*run.Conclusion) {
			return checksFailed, nil
		}
	}
	return checksPassed, nil
}

func isOptionalAPIError(err error) bool {
	return err == errNotFound
}

func allowedConclusion(conclusion string) bool {
	switch conclusion {
	case "success", "neutral", "skipped":
		return true
	default:
		return false
	}
}

func hasMergeConflict(pr pullRequest) bool {
	if pr.Mergeable != nil && !*pr.Mergeable {
		return true
	}
	switch pr.MergeableState {
	case "dirty":
		return true
	default:
		return false
	}
}

func containsLogin(logins []string, login string) bool {
	for _, item := range logins {
		if strings.EqualFold(strings.TrimPrefix(item, "@"), login) {
			return true
		}
	}
	return false
}

func markerComment(pr pullRequest, reason string, message string) string {
	return fmt.Sprintf("<!-- pr-bot-automerge:%s:%s -->\n%s", reason, pr.Head.SHA, message)
}

func maintainerComment(cfg Config, pr pullRequest, reason string, message string) string {
	mentions := make([]string, 0, len(cfg.Maintainers))
	for _, maintainer := range cfg.Maintainers {
		mentions = append(mentions, "@"+strings.TrimPrefix(maintainer, "@"))
	}
	return markerComment(pr, reason, strings.Join(mentions, " ")+"\n\n"+message)
}

func postCommentOnce(ctx context.Context, gh *githubClient, cfg Config, repo string, number int, body string, logger *log.Logger) error {
	marker := firstLine(body)
	comments, err := gh.listComments(ctx, repo, number)
	if err != nil {
		return err
	}
	for _, comment := range comments {
		if strings.Contains(comment.Body, marker) {
			logger.Printf("skip duplicate comment on PR #%d", number)
			return nil
		}
	}
	if cfg.DryRun {
		logger.Printf("dry run: would comment on PR #%d: %s", number, strings.ReplaceAll(body, "\n", " | "))
		return nil
	}
	if err := gh.createComment(ctx, repo, number, body); err != nil {
		return err
	}
	logger.Printf("commented on PR #%d", number)
	return nil
}

func firstLine(value string) string {
	line, _, _ := strings.Cut(value, "\n")
	return line
}
