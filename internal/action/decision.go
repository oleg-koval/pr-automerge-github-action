package action

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"
	"time"
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
	syntheticEvent := eventName == "schedule" || eventName == "workflow_dispatch"
	if !syntheticEvent && eventName != "pull_request" && eventName != "pull_request_target" && eventName != "pull_request_review" && eventName != "pull_request_review_comment" && eventName != "check_suite" {
		logger.Printf("not a supported PR event: %s", eventName)
		return nil
	}

	var payload eventPayload
	var eventPRs []eventPullRequest
	var err error
	requestedPRNumber := strings.TrimSpace(env.input("pr-number"))
	if !syntheticEvent {
		payload, err = loadEvent(env.get("GITHUB_EVENT_PATH"))
		if err != nil {
			return err
		}
	}
	if requestedPRNumber != "" {
		number, parseErr := strconv.Atoi(requestedPRNumber)
		if parseErr != nil || number <= 0 {
			return fmt.Errorf("pr-number input must be a positive integer, got %q", requestedPRNumber)
		}
		if !syntheticEvent {
			associated := false
			for _, pr := range payload.pullRequests() {
				if pr.Number == number {
					associated = true
					break
				}
			}
			if !associated {
				return fmt.Errorf("pr-number %d is not associated with the %s event", number, eventName)
			}
		}
		eventPRs = []eventPullRequest{{Number: number}}
	} else if syntheticEvent {
		return fmt.Errorf("pr-number input is required for %s events", eventName)
	} else {
		eventPRs = payload.pullRequests()
		if len(eventPRs) == 0 {
			logger.Printf("event has no pull request payload")
			return nil
		}
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

	var processErrors []error
	for _, eventPR := range eventPRs {
		if err := processPullRequest(ctx, env, gh, cfg, repo, eventPR, logger); err != nil {
			processErrors = append(processErrors, fmt.Errorf("process PR #%d: %w", eventPR.Number, err))
		}
	}
	return errors.Join(processErrors...)
}

func processPullRequest(ctx context.Context, env env, gh *githubClient, cfg Config, repo string, eventPR eventPullRequest, logger *log.Logger) error {
	pr, err := gh.getPullRequestWithMergeability(ctx, repo, eventPR.Number)
	if err != nil {
		return err
	}
	if pr.Number == 0 {
		pr.Number = eventPR.Number
	}
	if pr.User.Login == "" {
		pr.User.Login = eventPR.User.Login
	}
	if pr.Head.SHA == "" {
		pr.Head.SHA = eventPR.Head.SHA
	}
	if pr.State != "" && pr.State != "open" {
		logger.Printf("skip PR #%d because it is %s", pr.Number, pr.State)
		return nil
	}
	if pr.Draft || eventPR.Draft {
		logger.Printf("skip draft PR #%d", pr.Number)
		return nil
	}
	if !containsLogin(cfg.Bots, pr.User.Login) {
		logger.Printf("skip PR #%d from non-configured author %s", pr.Number, pr.User.Login)
		return nil
	}

	state, err := waitForChecks(ctx, gh, cfg, repo, pr.Head.SHA, env.get("GITHUB_RUN_ID"), logger)
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

	if isBlockedByChecks(pr) {
		logger.Printf("PR #%d is blocked by branch protection (required checks may not have run yet); skipping until check_suite fires", pr.Number)
		return nil
	}

	if isBehindBase(pr) {
		if cfg.DryRun {
			logger.Printf("dry run: would update branch for PR #%d", pr.Number)
			return nil
		}
		if err := gh.updateBranch(ctx, repo, pr.Number); err != nil {
			if pr.User.Login == "dependabot[bot]" {
				body := markerComment(pr, "dependabot-behind", cfg.DependabotRebaseComment)
				return postCommentOnce(ctx, gh, cfg, repo, pr.Number, body, logger)
			}
			body := maintainerComment(cfg, pr, "branch-update-failed", "GitHub refused to update this maintenance bot PR with the base branch. Please review it manually.\n\nError:\n\n```\n"+err.Error()+"\n```")
			return postCommentOnce(ctx, gh, cfg, repo, pr.Number, body, logger)
		}
		logger.Printf("updated branch for PR #%d", pr.Number)
		return nil
	}

	if pr.Mergeable == nil {
		body := maintainerComment(cfg, pr, "unknown-mergeability", "GitHub did not report whether this maintenance bot PR is mergeable. Please review it manually.")
		return postCommentOnce(ctx, gh, cfg, repo, pr.Number, body, logger)
	}

	openReviewComments, err := waitForReviewComments(ctx, gh, cfg, repo, pr.Number, logger)
	if err != nil {
		body := maintainerComment(cfg, pr, "review-comments-unknown", "GitHub did not report whether this maintenance bot PR has open review comments, so automerge refused to merge it. Please review it manually.\n\nError:\n\n```\n"+err.Error()+"\n```")
		return postCommentOnce(ctx, gh, cfg, repo, pr.Number, body, logger)
	}
	if openReviewComments {
		logger.Printf("skip PR #%d because it still has open review comments after waiting %s", pr.Number, cfg.WaitTimeout)
		return nil
	}

	body := mergeApprovedComment(pr, cfg.MergeMethod)
	if err := postCommentOnce(ctx, gh, cfg, repo, pr.Number, body, logger); err != nil {
		return err
	}

	// Re-read before the merge request. The earlier check can become stale while the
	// approval comment is being posted; branch protection should also require
	// conversation resolution for an atomic server-side merge boundary.
	openReviewComments, err = waitForReviewComments(ctx, gh, cfg, repo, pr.Number, logger)
	if err != nil {
		body := maintainerComment(cfg, pr, "review-comments-unknown-before-merge", "GitHub did not report whether this maintenance bot PR still has open review comments immediately before merge, so automerge refused to merge it. Please review it manually.\n\nError:\n\n```\n"+err.Error()+"\n```")
		return postCommentOnce(ctx, gh, cfg, repo, pr.Number, body, logger)
	}
	if openReviewComments {
		logger.Printf("skip PR #%d because review comments remain open before merge", pr.Number)
		return nil
	}

	latestPR, err := gh.getPullRequestWithMergeability(ctx, repo, pr.Number)
	if err != nil {
		return err
	}
	if latestPR.Head.SHA != pr.Head.SHA {
		logger.Printf("skip PR #%d because its head changed from %s to %s while it was being checked", pr.Number, pr.Head.SHA, latestPR.Head.SHA)
		return nil
	}
	if latestPR.Mergeable == nil || !*latestPR.Mergeable || hasMergeConflict(latestPR) || isBlockedByChecks(latestPR) || isBehindBase(latestPR) {
		logger.Printf("skip PR #%d because its refreshed merge state is no longer safe: mergeable=%v state=%s", pr.Number, latestPR.Mergeable, latestPR.MergeableState)
		return nil
	}

	// Keep one final review-thread read immediately before the merge request.
	openReviewComments, err = gh.hasOpenReviewComments(ctx, repo, pr.Number)
	if err != nil {
		body := maintainerComment(cfg, pr, "review-comments-unknown-before-merge", "GitHub did not report whether this maintenance bot PR still has open review comments immediately before merge, so automerge refused to merge it. Please review it manually.\n\nError:\n\n```\n"+err.Error()+"\n```")
		return postCommentOnce(ctx, gh, cfg, repo, pr.Number, body, logger)
	}
	if openReviewComments {
		logger.Printf("skip PR #%d because review comments opened after the final retry", pr.Number)
		return nil
	}

	if cfg.DryRun {
		logger.Printf("dry run: would merge PR #%d with method %s", pr.Number, cfg.MergeMethod)
		return nil
	}
	if err := gh.mergePullRequest(ctx, repo, pr.Number, cfg.MergeMethod, pr.Head.SHA); err != nil {
		if errors.Is(err, errMergeHeadChanged) {
			logger.Printf("skip PR #%d because its head changed before merge; it can be retried on a later event", pr.Number)
			return nil
		}
		latestPR, stateErr := gh.getPullRequest(ctx, repo, pr.Number)
		if stateErr == nil && latestPR.Merged {
			logger.Printf("PR #%d is already merged after a concurrent merge; treating merge as successful", pr.Number)
			return nil
		}
		body := maintainerComment(cfg, pr, "merge-failed", "GitHub refused to merge this maintenance bot PR. Please review it manually.\n\nError:\n\n```\n"+err.Error()+"\n```")
		return postCommentOnce(ctx, gh, cfg, repo, pr.Number, body, logger)
	}
	logger.Printf("merged PR #%d with method %s", pr.Number, cfg.MergeMethod)
	return nil
}

const (
	minReviewPollInterval = time.Second
	maxReviewPollInterval = 5 * time.Minute
)

func waitForReviewComments(ctx context.Context, gh *githubClient, cfg Config, repo string, number int, logger *log.Logger) (bool, error) {
	if cfg.WaitTimeout == 0 {
		return gh.hasOpenReviewComments(ctx, repo, number)
	}

	deadline := time.Now().Add(cfg.WaitTimeout)
	pollInterval := cfg.WaitInterval
	if pollInterval < minReviewPollInterval {
		pollInterval = minReviewPollInterval
	}
	for {
		if !time.Now().Before(deadline) {
			return true, nil
		}

		open, err := gh.hasOpenReviewComments(ctx, repo, number)
		if err != nil {
			var rateLimitErr *githubRateLimitError
			if !errors.As(err, &rateLimitErr) {
				return false, err
			}
			wait := pollInterval
			if !rateLimitErr.resetAt.IsZero() {
				wait = time.Until(rateLimitErr.resetAt)
			}
			if wait <= 0 {
				wait = pollInterval
			}
			if remaining := time.Until(deadline); wait > remaining {
				logger.Printf("review-thread lookup rate-limited for PR #%d beyond wait-timeout; skipping", number)
				return true, nil
			}
			logger.Printf("review-thread lookup rate-limited for PR #%d; waiting %s before retrying", number, wait)
			if err := waitForReviewRetry(ctx, wait); err != nil {
				return true, err
			}
			continue
		}
		if !time.Now().Before(deadline) {
			return true, nil
		}
		if !open {
			return false, nil
		}

		remaining := time.Until(deadline)
		wait := pollInterval
		if wait > remaining {
			wait = remaining
		}
		logger.Printf("review comments open for PR #%d; waiting %s before retrying", number, wait)
		if err := waitForReviewRetry(ctx, wait); err != nil {
			return true, err
		}
		if pollInterval < maxReviewPollInterval {
			pollInterval *= 2
			if pollInterval > maxReviewPollInterval {
				pollInterval = maxReviewPollInterval
			}
		}
	}
}

func waitForReviewRetry(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func waitForChecks(ctx context.Context, gh *githubClient, cfg Config, repo string, sha string, currentRunID string, logger *log.Logger) (checkState, error) {
	deadline := time.Now().Add(cfg.WaitTimeout)
	for {
		state, err := evaluateChecks(ctx, gh, repo, sha, currentRunID, cfg.IgnoredCheckNames)
		if err != nil || state != checksPending || cfg.WaitTimeout == 0 || time.Now().After(deadline) {
			return state, err
		}
		logger.Printf("checks pending for %s; waiting %s", sha, cfg.WaitInterval)
		select {
		case <-ctx.Done():
			return checksPending, ctx.Err()
		case <-time.After(cfg.WaitInterval):
		}
	}
}

type statusIdentity struct {
	context  string
	producer string
}

type checkRunIdentity struct {
	name     string
	producer string
}

func evaluateChecks(ctx context.Context, gh *githubClient, repo string, sha string, currentRunID string, ignoredCheckNames []string) (checkState, error) {
	status, err := gh.getCombinedStatus(ctx, repo, sha)
	if err != nil && !isOptionalAPIError(err) {
		if errors.Is(err, errPaginationChanged) {
			return checksPending, nil
		}
		return checksFailed, err
	}
	runs, err := gh.getCheckRuns(ctx, repo, sha)
	if err != nil && !isOptionalAPIError(err) {
		if errors.Is(err, errPaginationChanged) {
			return checksPending, nil
		}
		return checksFailed, err
	}
	if len(status.Statuses) == 0 && (status.State == "failure" || status.State == "error") {
		return checksFailed, nil
	}
	latestStatuses := make(map[statusIdentity]commitStatus, len(status.Statuses))
	for index, commit := range status.Statuses {
		key := statusIdentity{context: commit.Context, producer: statusProducer(commit, index)}
		latest, exists := latestStatuses[key]
		if !exists || commit.CreatedAt.After(latest.CreatedAt) || (commit.CreatedAt.Equal(latest.CreatedAt) && commit.ID > latest.ID) {
			latestStatuses[key] = commit
		}
	}
	statusesPending := false
	for _, commit := range latestStatuses {
		switch commit.State {
		case "success":
		case "pending":
			statusesPending = true
		default:
			return checksFailed, nil
		}
	}
	if statusesPending {
		return checksPending, nil
	}
	latestRuns := make(map[checkRunIdentity]checkRun, len(runs.CheckRuns))
	for index, run := range runs.CheckRuns {
		if isCurrentRun(run, currentRunID) || isIgnoredCheck(ignoredCheckNames, run.Name) {
			continue
		}
		key := checkRunIdentity{name: run.Name, producer: checkRunProducer(run, index)}
		latest, exists := latestRuns[key]
		if !exists || run.ID > latest.ID {
			latestRuns[key] = run
		}
	}
	if len(latestRuns) == 0 && len(status.Statuses) == 0 {
		return checksPending, nil
	}
	runsPending := false
	for _, run := range latestRuns {
		if run.Status != "completed" || run.Conclusion == nil {
			runsPending = true
			continue
		}
		if !allowedConclusion(*run.Conclusion) {
			return checksFailed, nil
		}
	}
	if runsPending {
		return checksPending, nil
	}
	return checksPassed, nil
}

func isOptionalAPIError(err error) bool {
	return err == errNotFound
}

func statusProducer(status commitStatus, index int) string {
	if status.Creator.ID != 0 {
		return fmt.Sprintf("user-id:%d", status.Creator.ID)
	}
	if status.Creator.Login != "" {
		return "user-login:" + strings.ToLower(status.Creator.Login)
	}
	if status.ID != 0 {
		return fmt.Sprintf("status-id:%d", status.ID)
	}
	return fmt.Sprintf("unknown-status:%d", index)
}

func checkRunProducer(run checkRun, index int) string {
	if run.App != nil && run.App.ID != 0 {
		return fmt.Sprintf("app-id:%d", run.App.ID)
	}
	if run.ID != 0 {
		return fmt.Sprintf("check-run-id:%d", run.ID)
	}
	return fmt.Sprintf("unknown-check-run:%d", index)
}

func isCurrentRun(run checkRun, currentRunID string) bool {
	if currentRunID == "" {
		return false
	}
	parsedURL, err := url.Parse(run.DetailsURL)
	if err != nil {
		return false
	}
	parts := strings.Split(strings.Trim(parsedURL.Path, "/"), "/")
	for index := 0; index+2 < len(parts); index++ {
		if parts[index] == "actions" && parts[index+1] == "runs" && parts[index+2] == currentRunID {
			return true
		}
	}
	return false
}

func isIgnoredCheck(ignored []string, name string) bool {
	for _, ignoredName := range ignored {
		if ignoredName == name {
			return true
		}
	}
	return false
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

func isBlockedByChecks(pr pullRequest) bool {
	return pr.MergeableState == "blocked"
}

func isBehindBase(pr pullRequest) bool {
	return pr.MergeableState == "behind"
}

func containsLogin(logins []string, login string) bool {
	for _, item := range logins {
		if strings.EqualFold(strings.TrimPrefix(item, "@"), login) {
			return true
		}
	}
	return false
}

func mergeApprovedComment(pr pullRequest, mergeMethod string) string {
	message := fmt.Sprintf("Automerge checks passed for this maintenance bot PR. The action will attempt the merge using the %s method after a final head and safety recheck; this message does not confirm that a merge occurred.\n\nManaged by [oleg-koval/pr-automerge-github-action](https://github.com/oleg-koval/pr-automerge-github-action).", mergeMethod)
	return markerComment(pr, "merge-approved", message)
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
