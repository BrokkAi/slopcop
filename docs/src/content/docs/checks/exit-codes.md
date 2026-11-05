---
title: Exit codes
description: "The closed exit-code contract of the slopcop CLI, what each code means, and what a script or agent should do with it."
sidebar:
  order: 3
---

The CLI merges the hosted static status and the local review status.
Interrupts win, then findings, a static refusal needing a person, failure
or incompleteness, static pending, and clean.
`--if-enabled` can skip one locally, with `status: skipped` and exit `0`. A
script or an agent can act on the code alone for check completion; use
`approved` or the approval ref to decide whether the tree may be pushed.

| Exit | Meaning | What to do |
| --- | --- | --- |
| `0` | The check completed and no new finding crossed `--fail-on`; or `--if-enabled` found the repository inactive and sent nothing. | Carry on. On an empty change or a skip, nothing was checked; see below. |
| `1` | A new finding crossed `--fail-on`. | Fix the new findings and run the check again. |
| `2` | No new finding crossed `--fail-on`, but some rule did not complete; or the CLI or service failed. | With a report: say which rules did not complete, and why; running again usually gives the same answer. Without one: read the code on stderr. Never treat it as clean. |
| `3` | The check was still running when `--timeout` ran out. | Run the same check again; it picks up the same analysis. |
| `4` | The service refused the check, and only a person can clear it. | Do not retry. Tell the person the `code` and remedy. |
| `130`, `143` | Interrupted (Ctrl-C, or SIGTERM) before the check answered. No report was printed. | Run the same check again. |

Exit `0` can also include a skipped static half or an empty review. Inspect the
report before calling the change checked. A status this CLI does not know exits
`2`, so a newer service can never pass a check by accident, and stderr names the
status. An inconclusive rule never changes the exit code (see [Read a
report](/checks/read-a-report/#inconclusive-checks)).

In JSON, top-level `status` matches the exit code. `staticStatus` keeps
the service status when it differs. `review.status` is `clean`, `findings`,
`failed`, or `empty`; `review.reviewers` explain the outcome, with an error on
each reviewer that could not run. Review findings count toward exit `1` unless `--fail-on never` is
set. A complete review is reused when its inputs have not changed.

## Exit 1: findings

New findings that cross `--fail-on` exit `1` even when some rule did not
complete. The findings are the work: fix them in the working tree and run the
same check again. Exit `2` remains incomplete even when `summary.newFindings`
is `0`.

## Which results approve the tree

A check records `refs/slopcop/reviewed/<tree>` when a nonempty change has a
clean review (or no review), and the static half completed with no new finding
at or above `gate.blockOn` (or was skipped or did not run). The default push
level is `error`; warnings and notes are advisory even when `--fail-on` makes
them exit `1`. `--fail-on` changes the exit code, not push approval. A static
rule may be incomplete while the completed findings still approve the tree.
A failed review, static failure, refusal, pending or unknown status never
approves it. A later check that does not approve the same nonempty tree removes
its approval ref, including an override. Rerun `slopcop override` if the review
findings are unchanged. JSON `approved` reports the decision directly.

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
The CLI also exits `4` with `repository_not_enabled` when the workspace has not
added the repository: it refuses locally, before sending anything, so a check
never adds one. Scripts stop instead of retrying.
[Error codes](/reference/error-codes/) lists the remedies.

## An empty change

A check whose patch is empty, because the working tree matches the base, still
exits by the service's status, usually `0`. The report carries
`"emptyChange": true` and stderr says so. A script that must check committed
work passes `--base`; an agent reads the flag and does the same. See
[Run a check](/checks/run-a-check/#an-empty-patch).

## A skipped repository

`--if-enabled` exits `0` without sending a check when the repository is not
active in the workspace. The machine formats carry `"status": "skipped"` and
the reason, so it cannot be mistaken for a clean check. A plain check of a
repository the workspace has not added exits `4` with
`repository_not_enabled` instead.
