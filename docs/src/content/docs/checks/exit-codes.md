---
title: Exit codes
description: "The closed exit-code contract of the slopcop CLI, what each code means, and what a script or agent should do with it."
sidebar:
  order: 3
---

The service sets the status and the exit code follows it. A script or an agent
can act on the code alone.

| Exit | Meaning | What to do |
| --- | --- | --- |
| `0` | The check completed and no new finding crossed `--fail-on`. | Carry on. On an empty change this means nothing was checked; see below. |
| `1` | A new finding crossed `--fail-on`. | Fix the new findings and run the check again. |
| `2` | No new finding crossed `--fail-on`, but some rule did not complete; or the CLI or service failed. | With a report: say which rules did not complete, and why; running again usually gives the same answer. Without one: read the code on stderr. Never treat it as clean. |
| `3` | The check was still running when `--timeout` ran out. | Run the same check again; it picks up the same analysis. |
| `4` | The service refused the check, and only a person can clear it. | Do not retry. Tell the person the `code` and remedy. |
| `130`, `143` | Interrupted (Ctrl-C, or SIGTERM) before the check answered. No report was printed. | Run the same check again. |

Only `clean` exits `0`. A status this CLI does not know exits `2`, so a newer
service can never pass a check by accident, and stderr names the status. An
inconclusive rule never changes the exit code (see
[Read a report](/checks/read-a-report/#inconclusive-checks)).

## Exit 1: findings

New findings that cross `--fail-on` exit `1` even when some rule did not
complete. The findings are the work: fix them in the working tree and run the
same check again. The loop ends when the check reports no new findings: exit
`0`, or exit `2` with a report whose `summary.newFindings` is `0`.

## Exit 2: incomplete, or an error

Two different things exit `2`:

- **A check report with incomplete rules.** Stdout has the report; its
  `incomplete` list names the rules that did not complete and why. Rerunning
  usually gives the same answer, so do not loop on it.
- **A failure.** Stdout is empty in `human`; in `json` it is a `failed` report
  with the service's `code`, `message`, `remedy`, and `retryable`. Stderr starts
  with `slopcop: <code>:`. Retry only when `retryable` is `true`. Any other
  error from the CLI itself exits `2` with an empty stdout.

## Exit 4: a person must act

A check exits `4` when the service refused it and only a person can clear the
refusal: signing in (`auth_required`, `auth_expired`), access to the repository
(`repo_not_indexed`, `app_not_installed`, `app_no_repo_access`,
`gitlab_not_connected`, `no_access`), credits (`insufficient_credits`), what the
workspace checks (`repository_disabled`, `repository_add_unconfirmed`,
`workspace_repository_limit_reached`, `active_repository_limit_reached`,
`owner_required`), or something only the browser can do (`browser_required`).
Scripts stop instead of retrying. [Error codes](/reference/error-codes/) lists
the remedies.

## An empty change

A check whose patch is empty, because the working tree matches the base, still
exits by the service's status, usually `0`. The report carries
`"emptyChange": true` and stderr says so. A script that must check committed
work passes `--base`; an agent reads the flag and does the same. See
[Run a check](/checks/run-a-check/#an-empty-patch).
