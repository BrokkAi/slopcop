---
title: Exit codes
description: "The closed exit-code contract of the slopcop CLI, what each code means, and what a script or agent should do with it."
sidebar:
  order: 3
---

The CLI merges the hosted static status and the local review status.
Interrupts win, then findings, a static refusal needing a person, static
failure or incompleteness, static pending, a failed review, and clean. A
failed review with a pending static half exits `3`, because running the check
again also runs the review again; `review.status` still says `failed`.
`--if-enabled` can skip one locally, with `status: skipped` and exit `0`. A
script or an agent can act on the code alone for check completion; use
`approved` or the approval ref to decide whether the tree may be pushed.

| Exit | Meaning | What to do |
| --- | --- | --- |
| `0` | The check completed and no new finding crossed `--fail-on`; or `--if-enabled` found the repository disabled or past the active limit and sent nothing. | Carry on. On an empty change or a skip, nothing was checked; see below. |
| `1` | A new static finding crossed `--fail-on`, or a reviewer reported a finding (under any `--fail-on` but `never`). JSON `exitReason` says which. | Fix the new findings and run the check again. |
| `2` | No new finding crossed `--fail-on`, but some rule did not complete; or the CLI or service failed. | With a report: say which rules did not complete, and why; running again usually gives the same answer. Without one: read the code on stderr. Never treat it as clean. |
| `3` | The check was still running when `--timeout` ran out. | Run the same check again; it picks up the same analysis. |
| `4` | The service refused the check, and only a person can clear it. | Do not retry. Tell the person the `code` and remedy. |
| `130`, `143` | Interrupted (Ctrl-C, or SIGTERM) before the check answered. No report was printed. | Run the same check again. |

Exit `0` can also include a skipped static half or an empty review. Inspect the
report before calling the change checked. A status this CLI does not know exits
`2`, so a newer service can never pass a check by accident, and stderr names the
status. An inconclusive rule never changes the exit code (see [Read a
report](/checks/read-a-report/#inconclusive-checks)).

In JSON, top-level `status` matches the exit code, except that it is
`findings` whenever the change has new static or review findings, even when
`--fail-on` lets them exit `0` (below the threshold, or `--fail-on never`).
`staticStatus` keeps the static half's status when it differs. `review.status` is `clean`, `findings`,
`failed`, or `empty`; `review.reviewers` explain the outcome, with an error on
each reviewer that could not run, `firstError` and `firstLog` on one that
was retried, and `standsInFor` on a fallback. A review is clean when at least
`review.minCompleted` reviewers (default 2, at most the number configured)
finished clean and the others failed after their retry. `review.incomplete`
is true when fewer finished; with findings, that still exits `2` under
`--fail-on never`. Review findings count toward exit `1` unless `--fail-on never` is
set. A complete review is reused when its inputs have not changed. Its inputs are the
reviewed content, the reviewers, the slopcop version and the intent, the
user's messages in the session. A clean review of a tree that is still
approved for push is also reused when only the intent changed, such as after
asking the agent to amend a commit message or squash commits.

## Exit 1: findings

New findings that cross `--fail-on` exit `1` even when some rule did not
complete. The findings are the work: fix them in the working tree and run the
same check again. Exit `2` remains incomplete even when `summary.newFindings`
is `0`.

In JSON, `exitReason` says which findings made the check exit `1`:
`static_findings`, `review_findings`, or `static_and_review_findings`. It is
absent for every other exit.

A reviewer's finding fails the check by default, under every `--fail-on`
level but `never`. Review findings carry a priority (`P0` to `P3`), not a
static severity, so `note`, `warning` and `error` do not filter them. They fail
by default because a script, CI job or agent loop that reads only the exit code
would otherwise pass a change a reviewer flagged. The static counts in
`summary` (`findings`, `newFindings`) never include them, so a check the
review alone fails can show `summary.findings: 0` and `staticStatus: clean`
and still exit `1`. JSON `exitReason` names what failed it, and stderr says so
in every format, with the count and where the review findings are listed.
Fix the real ones and check again. For a wrong one, run
`slopcop override <rv-id> --reason "..."` and check again: the reused review
leaves it out, so the check passes, and later reviews do not report it.
`slopcop override --reason "..."` without an id approves this tree for push
with all its findings, but does not change the exit code; `--fail-on never`
keeps review findings, and static ones, from exiting `1`.

## Which results approve the tree

A check records `refs/slopcop/reviewed/<tree>` when a nonempty change has a
clean review (or no review), and the static half completed with no new finding
at or above `gate.blockOn` (or was skipped or did not run). The default push
level is `error`, or [the repository's threshold](#the-repositorys-threshold)
when that is stricter; findings below it are advisory even when `--fail-on`
makes them exit `1`. `--fail-on` changes the exit code, not push approval. A static
rule may be incomplete while the completed findings still approve the tree,
but a run failure (`runFailures` in `json`) does not: it left every check of
an analyzer run without an answer, so that run checked nothing. Run the check
again. If the run keeps failing the same way, `slopcop override --reason "..."`
approves this tree only, with the reason recorded, when the review passed (or
its findings are accepted with it) and no static finding blocks the push;
stderr says so in every format. Report the failure to SlopCop too.
A failed review, static failure, refusal, pending or unknown status never
approves it.

A review-only check (`--mode ai`) approves a tree only where the static half
cannot run: the checkout has no origin the service can name, the CLI has no
login, or origin has no branches yet. Where the static half could have run,
a passing review-only check reports `approved: false`, stderr says why in
every format, and `slopcop override --reason` will not approve the tree on that
review either: run `slopcop check` so both halves check it, or
`slopcop check --mode static`. An approval a review-only check gave is marked
with `refs/slopcop/review-only/<tree>`, and the push gate refuses it once the
static half can run, such as after logging in again or once the first push has
given origin a branch. A later check that does not approve the same nonempty tree removes
its approval ref, including an override. Rerun `slopcop override` if the review
findings are unchanged. JSON `approved` reports the decision directly.

## The push gate and pull request checks

Three thresholds judge the same new static findings:

| Gate | Fails on | Set with |
| --- | --- | --- |
| The exit code of `slopcop check` | new findings at or above `--fail-on`; default the repository's threshold, else `finding`, any new finding | the flag |
| Push approval: `approved`, and the push gate | new findings at or above `gate.blockOn`; default `error`, or the repository's threshold when that is stricter | the [user config file](/reference/configuration/#push-approval-level) |
| A [pull request check](/checks/pull-requests/) | new findings at or above the repository's threshold; without one, any new finding | the repository's threshold |

### The repository's threshold

A repository gives all three one threshold by committing `fail_on` in the
`[check]` section of `.slopcop/config.toml`:

```toml
[check]
# The least severe new finding that fails a check of this repository:
# never, finding, note, warning or error.
fail_on = "warning"
```

The service reads it from the code it checks, like the rest of
[`[check]`](/checks/false-positives/#exclude-paths-from-checks), so pull
request checks, the MCP `check` tool and `slopcop check` read the same value,
and a change to it takes effect in the change that makes it. With
`fail_on = "warning"`, a new `note` exits `0`, is approved for push and passes
the pull request check; a new `warning` exits `1`, blocks the push and fails
the pull request check.

- `--fail-on` still wins for its own check, so a script that passes it gets
  the exit code it asked for.
- It rates static severities only. Local review findings still fail the
  check under `fail_on = "never"`; only a `--fail-on never` you pass stops
  them.
- The threshold can raise the default push level, never lower it: a change
  can edit its own `fail_on`, and what blocks a push stays a person's to
  loosen. A `gate.blockOn` set in the user config file stands as it is.
- A change that sets, removes or alters `fail_on` says so, in the report
  (`repositoryFailOn` in `json`, `slopcop.repositoryFailOn` in `sarif`) and
  on the pull request, so a change that lowers its own bar is seen in review.

Without a repository threshold, a new `note` or `warning` exits `1`, is
approved for push, and fails the pull request check. The exit code and the
pull request check agree; push approval is the looser one. An `info` finding
fails none of the three.

A check that approves a tree with findings a pull request check fails on says
so. `advisoryFindings` in `json` (`slopcop.advisoryFindings` in `sarif`)
counts them, and where the push gate is installed stderr says, in every
format, that a pull request check fails on them. Before you open the pull
request, fix them or [ignore the false positives](/checks/false-positives/).
To have the push gate block what the pull request check fails on, commit the
repository's threshold, or set `gate.blockOn` to the same level;
`advisoryFindings` is then never present.

None of the three treats a test or fixture path differently from any other
path. What a check leaves out is decided by
[`[check]` in `.slopcop/config.toml`](/checks/false-positives/#exclude-paths-from-checks),
so a path excluded there is excluded from all three alike. For test inputs
that are small or wrong on purpose, name the style rules in an
`exclude_rules` entry for their paths: every other rule, the security rules
among them, keeps checking those files.

## Exit 2: incomplete, or an error

Two different things exit `2`:

- **A check report with incomplete rules.** Stdout has the report; its
  `incomplete` list names the rules that did not complete and why. In every
  format, stderr also names up to five of them with their reasons, such as
  `slopcop: the check is incomplete: 1 policy didn't finish:
  bifrost.correctness.loop-body-never-repeats (timed out).` A rule that did not
  finish did not check the code, whatever the findings count says. Rerunning
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
The CLI exits `4` with `repository_disabled` itself when the workspace has
disabled the repository, and with `active_repository_limit_reached` when it is
past the active limit: it refuses locally, before sending anything or starting
the local reviewers. A repository the workspace has never added is added by
the check instead. Scripts stop instead of retrying.
[Error codes](/reference/error-codes/) lists the remedies.

## An empty change

A check whose patch is empty, because the working tree matches the base, still
exits by the service's status, usually `0`. The report carries
`"emptyChange": true` and stderr says so. A script that must check committed
work passes `--base`; an agent reads the flag and does the same. See
[Run a check](/checks/run-a-check/#an-empty-patch).

## A skipped repository

`--if-enabled` exits `0` without sending a check when the workspace has
disabled the repository or has it past the active limit. The machine formats
carry `"status": "skipped"` and the reason, so it cannot be mistaken for a
clean check. A plain check of such a repository exits `4` instead.
