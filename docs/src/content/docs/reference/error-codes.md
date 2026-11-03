---
title: Error codes
description: "The closed set of codes a failed check or command can answer with, what each means, whether it is retryable, and what to do."
sidebar:
  order: 3
---

A failed request prints the service's message, remedy, and any install or
top-up link verbatim. In `human`, nothing goes to stdout and stderr starts with
`slopcop: <code>:`; in `json`, stdout is
`{"status": "failed", "code": …, "message": …, "remedy": …, "retryable": …}`,
plus `installUrl` or `topUpUrl` when the service sent one. Retry only when
`retryable` is `true`.

A check exits `4` when only a person can clear the refusal, and `2` otherwise.
The remedies name no client, so the CLI adds its own command after some, such
as `slopcop login` when the remedy is to sign in.

## Sign-in

| Code | Exit | What to do |
| --- | --- | --- |
| `auth_required`, `auth_expired` | 4 | Sign in again with `slopcop login`, or set `SLOPCOP_TOKEN`. |
| `account_required` | 4 | The token is not tied to an account. Sign in from a browser where you are signed in to SlopCop. |
| `device_flow_disabled` | 2 | This deployment has no browser login; use `slopcop login --token`. |
| `browser_required` | 4 | Only the web app can do this, such as enabling a private Bitbucket or GitLab repository. |

## Repository access

| Code | Exit | What to do |
| --- | --- | --- |
| `repo_not_indexed` | 4 | The service has no clone of the repository yet. A public one is cloned on first use; a private one needs the [GitHub App or a provider connection](/account/private-repositories/). |
| `app_not_installed` | 4 | Install the SlopCop GitHub App on the repository's owner, at the printed `installUrl`. |
| `app_no_repo_access` | 4 | The App is installed but this repository is not selected for it. |
| `gitlab_not_connected` | 4 | The workspace has no GitLab connection, or it needs reconnecting, in the web app. |
| `no_access` | 4 | Your account cannot read the repository, or has not authorized SlopCop with the provider. |
| `base_not_found` | 2 | The service's clone lacks the base commit; pass one origin has with `--base`. When the clone can tell why, a line after the remedy says so: origin's default branch was renamed and `origin/HEAD` still names the old one, or the base is not on origin's default branch. |
| `payload_too_large` | 2 | The change is over the service's limit; check a smaller change. |

## Workspace and billing

| Code | Exit | What to do |
| --- | --- | --- |
| `insufficient_credits` | 4 | The workspace is out of credits, so no new check runs. Add credits at the printed `topUpUrl`. See [Credits](/account/credits/). |
| `repository_disabled` | 4 | The workspace's owner disabled the repository. `slopcop whoami` names the workspace. |
| `repository_add_unconfirmed` | 4 | The check would add a repository the workspace then pays for. At a terminal, answer the prompt; otherwise, once approved, run the check again with `--add-repository --workspace <name\|id>`. See [Repositories](/account/repositories/). |
| `workspace_repository_limit_reached`, `active_repository_limit_reached` | 4 | The workspace is at its limit of repositories or active repositories. The owner chooses which stay active on the Repositories page, or unlocks the workspace. |
| `workspace_not_found` | 2 | Pick a name from `slopcop workspace list`, or pass the id. |
| `workspace_repository_not_found` | 2 | The repository is not in that workspace; `slopcop repo list` shows what is. |
| `owner_required` | 4 | Only the workspace's owner may do this. `--workspace` names the right workspace if the login is bound to another. |

## Findings

| Code | Exit | What to do |
| --- | --- | --- |
| `finding_not_found` | 2 | `slopcop ignore` or `unignore` got an id that is not from a check run in your workspace; use one a check printed. |

## Service

| Code | Exit | What to do |
| --- | --- | --- |
| `check_running` | handled | The analysis is still running. The CLI waits the `Retry-After` and asks again by the check's id until `--timeout`; then exit `3`. |
| `check_not_found` | handled | The service has no recent check with that id for this login. The CLI sends the change again. |
| `indexing_in_progress` | 2 | The service is still indexing the repository. Run the check again later. |
| `client_outdated` | 2 | This slopcop is older than the service accepts. [Upgrade](/get-started/install/#upgrade) and run the check again. |
| `rate_limited` | 2, retryable | Too many requests; wait the `Retry-After`. |
| `service_unavailable` | 2, retryable | Retry; the service is unavailable. |

## Login polling

`slopcop login` handles these itself: `slow_down` raises the polling interval;
`authorization_pending` keeps waiting; `expired_token` and `access_denied` end
the login with a message. A retryable failure, a dropped connection, or a
proxy's `429`, `502`, `503`, or `504` keep the code valid and the CLI polls
again until the code expires.

The codes, messages, and remedies are defined by the service, which the CLI
never rewrites, so a newer service can add a code: an unknown code exits `2`.
