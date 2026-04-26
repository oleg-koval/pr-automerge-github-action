# PR bot automerge GitHub Action

Automerge pull requests opened by configured maintenance bots. The action is PR-only, comments `@dependabot rebase` on Dependabot merge conflicts, and mentions configured maintainers when checks fail, non-Dependabot bot PRs conflict, or GitHub refuses the merge.

## Usage

```yaml
name: Bot automerge

on:
  pull_request_target:
    types: [opened, reopened, synchronize, ready_for_review, edited]

permissions:
  contents: write
  pull-requests: write
  checks: read
  statuses: read
  issues: write

jobs:
  automerge:
    runs-on: ubuntu-latest
    steps:
      - uses: oleg-koval/pr-automerge-github-action@v1
        with:
          github-token: ${{ github.token }}
          maintainer-handles: oleg-koval,octocat
```

## Inputs

| Input | Default | Description |
| --- | --- | --- |
| `github-token` | `${{ github.token }}` | Token used to read PR state, comment, and merge. |
| `bot-logins` | `dependabot[bot],snyk-bot,renovate[bot]` | Comma-separated bot logins allowed to automerge. |
| `maintainer-handles` | required | Comma-separated GitHub handles to mention for manual action. |
| `merge-method` | `squash` | One of `merge`, `squash`, or `rebase`. |
| `config-path` | `.github/pr-bot-automerge.yml` | Optional YAML config path. |
| `wait-timeout` | `30m` | Maximum time to wait for other checks before deciding. Set `0s` to fail fast. |
| `wait-interval` | `30s` | Poll interval while waiting for checks. |
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
```

## Behavior

The action exits successfully outside PR events. It ignores PRs not opened by allowed bot logins and ignores draft PRs. It waits for pending checks by default, then merges only when checks and statuses are successful and GitHub reports the PR as mergeable. If the automerge job lives in the same workflow as validation jobs, make it depend on those jobs with `needs`; if validation lives in separate workflows, the built-in wait loop handles it.

If checks fail, the action treats the update as potentially breaking and mentions maintainers. If a Dependabot PR has a merge conflict, it comments `@dependabot rebase`. If another bot PR has a conflict, it mentions maintainers. Before merging, it comments what it is doing with attribution to this action and a small celebration GIF. Duplicate comments for the same PR head SHA and reason are suppressed.

## Release

Push a semver tag to publish a GitHub release with GoReleaser:

```sh
git tag v1.0.0
git push origin v1.0.0
```

The release workflow runs formatting, module, test, lint, and Docker build checks before publishing binaries and checksums.
