# PR bot automerge GitHub Action

Automerge pull requests opened by configured maintenance bots. The action is PR-only, comments `@dependabot rebase` on Dependabot merge conflicts, and mentions configured maintainers when checks fail, non-Dependabot bot PRs conflict, or GitHub refuses the merge.

## Usage

Copy the complete, security-scoped workflow from [`examples/workflow.yml`](examples/workflow.yml) to `.github/workflows/automerge.yml`. It fans out one job per PR (including each PR associated with a `check_suite` event), serializes runs per PR, and pins the action to the immutable v1.5.0 commit SHA. Update `maintainer-handles` for your repository.

## Inputs

| Input | Default | Description |
| --- | --- | --- |
| `github-token` | `${{ github.token }}` | Token used to read PR state, comment, and merge. |
| `bot-logins` | `dependabot[bot],snyk-bot,renovate[bot]` | Comma-separated bot logins allowed to automerge. |
| `maintainer-handles` | required | Comma-separated GitHub handles to mention for manual action. |
| `merge-method` | `squash` | One of `merge`, `squash`, or `rebase`. |
| `config-path` | `.github/pr-bot-automerge.yml` | Optional YAML config path. |
| `pr-number` | unset | Pull request for a per-PR matrix run or a scheduled/manual retry; webhook events require it to be associated with that event. |
| `wait-timeout` | `30m` | Maximum time to wait for pending checks and open review threads before deciding. Set `0s` to fail fast. |
| `wait-interval` | `30s` | Initial poll interval while waiting for checks or open review threads; it backs off up to five minutes. |
| `ignored-check-names` | `automerge` | Comma-separated check run names ignored while deciding merge readiness. |
| `dry-run` | `false` | Log intended comments and merges without writing. |

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
```

## Behavior

The action ignores closed, merged, draft, and non-allowlisted PRs. It waits for checks by default, updates branches behind their base, and only requests a merge when checks/statuses pass, GitHub reports the PR mergeable, and every review thread is resolved. It rechecks review threads immediately before requesting the merge. A merge race that finds the PR already closed is treated as success.

GitHub Actions has no `pull_request_review_thread` trigger, so the example workflow retries every open PR every 10 minutes and supports manual retries with `pr-number`; it paginates discovery and fails loudly rather than silently truncating if more than the 256-job matrix limit is reached. If validation runs in the same workflow, use `needs` so automerge waits for it too. The scheduled retry handles checks from separate workflows.

## Security and branch protection

`pull_request_target` runs with the base repository's token and secrets. Never check out the PR head, execute PR-controlled scripts, or invoke other code supplied by the PR in this privileged workflow. Keep the bot allowlist narrow and grant write permissions only to the automerge job. Protect target branches with required status checks and required conversation resolution: API checks reduce risk, but GitHub branch protection is the server-side merge boundary for races between a check and the merge request. The example pins the action to the reviewed v1.5.0 commit SHA; update that full SHA only after reviewing a release.

If checks fail, the action treats the update as potentially breaking and mentions maintainers. If a Dependabot PR has a merge conflict, it comments `@dependabot rebase`. If another bot PR has a conflict, it mentions maintainers. Before merging, it comments what it is doing with attribution to this action and a small celebration GIF. Duplicate comments for the same PR head SHA and reason are suppressed.

## Release

Push a semver tag to publish a GitHub release with GoReleaser:

```sh
git tag v1.0.0
git push origin v1.0.0
```

The release workflow runs formatting, module, test, lint, and Docker build checks before publishing binaries and checksums.
