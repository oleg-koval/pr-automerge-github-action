# PR bot automerge GitHub Action

Automerge pull requests opened by configured maintenance bots. By default, the action comments on failures and successful merge attempts. Enable `no-pr-comments` to suppress every PR comment and require an empty PR discussion before merging.

## Usage

Copy the complete, security-scoped workflow from [`examples/workflow.yml`](examples/workflow.yml) to `.github/workflows/automerge.yml`. It fans out one job per PR (including each PR associated with a `check_suite` event), serializes runs per PR, and pins the action to the immutable reviewed commit SHA. Update `maintainer-handles` for your repository.

## Inputs

| Input | Default | Description |
| --- | --- | --- |
| `github-token` | `${{ github.token }}` | Token used to read PR state and merge; comment permissions are needed only when comment posting is enabled. |
| `bot-logins` | `dependabot[bot],snyk-bot,renovate[bot]` | Comma-separated bot logins allowed to automerge. |
| `maintainer-handles` | required | Comma-separated GitHub handles to mention for manual action. |
| `merge-method` | `squash` | One of `merge`, `squash`, or `rebase`. |
| `config-path` | `.github/pr-bot-automerge.yml` | Optional YAML config path. |
| `pr-number` | unset | Pull request for a per-PR matrix run or a scheduled/manual retry; webhook events require it to be associated with that event. |
| `wait-timeout` | `30m` | Maximum time to wait for pending checks and open review threads before deciding. Set `0s` to fail fast. |
| `wait-interval` | `30s` | Initial poll interval while waiting for checks or open review threads; it backs off up to five minutes. |
| `ignored-check-names` | `automerge` | Comma-separated exact names eligible for ignoring only after workflow-run provenance checks; matrix-expanded names must be listed exactly. |
| `automerge-workflow-path` | unset | Expected `.github/workflows/*.yml` file for the automerge workflow. Required to ignore own checks; if an ignored-name check is pending without this path, the action fails fast with a configuration error instead of waiting. The example sets `.github/workflows/automerge.yml`. |
| `dry-run` | `false` | Log intended comments and merges without writing. |
| `no-pr-comments` | `false` | Never add PR comments; skip merging while the PR has an issue comment, review comment, or non-empty review body. |

## Config file

Inputs override config file values when set.

```yaml
bots:
  - dependabot[bot]
  - snyk-bot
  - renovate[bot]
maintainers:
  - oleg-koval
  - octocat
merge_method: squash
dependabot_rebase_comment: "@dependabot rebase"
wait_timeout: 30m
wait_interval: 30s
ignored_check_names:
  - automerge
automerge_workflow_path: .github/workflows/automerge.yml
no_pr_comments: false
```

## Behavior

The action ignores closed, merged, draft, and non-allowlisted PRs. It waits for checks by default, updates branches behind their base, and only requests a merge when checks/statuses pass, GitHub reports the PR mergeable, and every review thread is resolved. Every distinct status and check run must pass; an old failed record can therefore block even after a newer success. For multi-page responses, two complete scans must agree; inconsistent snapshots remain pending. It refreshes the PR head and checks, then rechecks review threads immediately before requesting the merge. A 409 merge response is considered a stale head only if a subsequent PR read confirms a different SHA; otherwise maintainers are notified. If a merge request races with another actor, the action treats it as successful only when GitHub confirms the PR is merged.

With `no-pr-comments: true`, the action also checks the PR conversation comments, every review thread (including resolved threads), and review summary bodies before checks and again immediately before merging. API errors fail closed. The final check and merge request cannot be atomic, so a comment can race in after the last check.

GitHub Actions has no `pull_request_review_thread` trigger, so the example workflow retries every open PR every 10 minutes and supports manual retries with `pr-number`; it paginates discovery and fails loudly rather than silently truncating if the PR set would exceed 255 matrix entries plus the discovery job (256 total jobs). If validation runs in the same workflow, use `needs` so automerge waits for it too. The scheduled retry handles checks from separate workflows.

## Security and branch protection

`pull_request_target` runs with the base repository's token and secrets. Never check out the PR head, execute PR-controlled scripts, or invoke other code supplied by the PR in this privileged workflow. Keep the bot allowlist narrow and grant write permissions only to the automerge job; grant that job `actions: read` so the action can verify workflow-run provenance. Ignored names alone never exempt a check: the action requires a configured workflow path and verifies the Actions run's authoritative path, repository, head SHA, and check-suite ID. When provenance cannot be established, the check remains a gate. Protect target branches with required status checks and required conversation resolution: API checks reduce risk, but GitHub branch protection is the server-side merge boundary for races between a check and the merge request. The example pins the action to the immutable reviewed commit SHA; update that full SHA only after reviewing a release.

If checks fail, the action treats the update as potentially breaking and mentions maintainers. If a Dependabot PR has a merge conflict, it comments `@dependabot rebase`. If another bot PR has a conflict, it mentions maintainers. Before attempting a merge, it comments on the passing checks with action attribution; that notice is not a confirmation that the merge succeeded. Duplicate comments for the same PR head SHA and reason are suppressed.

## Release

Push a semver tag to publish a GitHub release with GoReleaser:

```sh
git tag v1.0.0
git push origin v1.0.0
```

The release workflow runs formatting, module, test, lint, and Docker build checks before publishing binaries and checksums.
