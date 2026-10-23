---
name: slopcop
description: Check a working-tree change with the slopcop CLI, which sends the change to the hosted SlopCop check API. Use when asked to run slopcop, to check a change for policy findings, to fix what a check reported, to report a finding that is a false positive, or to gate a change in CI.
---

# SlopCop

`slopcop` checks a change through the hosted check API. The CLI sends working-tree snapshots in a git
packfile; the service imports them, runs
the hosted policy catalog plus the repository rules selected, and returns the
report. There is no local analyzer, and the CLI never decides on its own that
there is nothing to check.

## Sign in

- `slopcop login` runs the browser device flow.
- `slopcop login --token "$SLOPCOP_TOKEN"` stores an existing credential, which
  is how CI and agents sign in. `$SLOPCOP_TOKEN` alone also works, with no login.
- `--service` or `$SLOPCOP_SERVICE` names another deployment; the default is
  `https://slopcop.com`. `slopcop logout` forgets the stored credential.

## Run a check

```console
slopcop                        # check the current checkout
slopcop check --format json    # machine-readable report
slopcop --root ../repo --base HEAD~1
```

The base commit defaults to the merge base with the branch upstream, then the
merge base with `origin/HEAD`, then `HEAD`. One request goes out per run, even
when nothing changed: an unchanged snapshot is still checked.

`--since` accepts a commit-ish or `merge-base` (BASE). It defaults to the last
completed run on this branch, recorded in `refs/slopcop/last-run/<branch>`,
else BASE. Completed runs, including findings and incomplete results, advance
that ref; pending checks and failed requests do not. Detached HEAD has no
marker. Use `--since merge-base` for CI branch comparisons. NOW and SINCE
snapshots each have BASE as their only parent, omitting intermediate history;
SINCE reuses BASE when their trees match. NOW includes untracked non-ignored
files and selected ignored policy files. The real index stays untouched.
Decoded packs above 16 MiB fail locally.

Flags worth knowing: `--format human|json|sarif` (default `human`),
`--fail-on never|finding|info|note|warning|error` (default `finding`, which
gates note or above; info is below note and appears in reports without failing
`finding`, while `--fail-on info` gates it; the service alone defaults to
`warning`), `--policy PATH` for a repository-relative `.rqlp` file (repeatable), and
`--no-policy-discovery` to drop the `.slopcop/policies/**/*.rqlp` rules that
otherwise join automatically. `--show-existing` also lists the findings that
were already in the code and those reported as false positives. `--timeout
DURATION` bounds the whole wait for a result (default 10 minutes) and
`--verbose` prints the resolved request to stderr.

Without `--rule` or `--category` includes, the default rules run. Otherwise,
the service runs the union of named rules and the default rules in named
categories. Then `--skip-rule` and `--skip-category` remove rules by ID or
category. Unknown names are an error. All four flags are repeatable; duplicate
values are sent once. There is no pack selector. Explicit `--policy PATH`
files and discovered repository policies continue to join the check.
Do not skip a rule to get past a finding you believe is wrong: report it as a
false positive instead (see below), so the team can see it and the rule can
be fixed.

## New, pre-existing, and dismissed findings

A check fails only on findings the change introduced or touched. Findings that
were already in the code are counted in `summary.existingFindings`, never
change the exit code, and are listed only with `--show-existing`. In `json`,
each listed finding has `new`; `summary.newFindings` counts the new ones. In
`sarif`, `baselineState` is `new` or `unchanged`.

A finding someone reported as a false positive in this repository is
dismissed: it comes back with `new: false` and `dismissed: true`, is counted in
`summary.dismissedFindings`, never changes the exit code, and is listed only
with `--show-existing`, under "Reported as false positive" (in `sarif`, as a
suppressed result).

Every finding has an id: the `Id:` line in `human`, `id` in `json`, and
`properties["slopcop.findingId"]` in `sarif`. The check itself has one too (`checkId`).

## Read the exit code

| Exit | Meaning |
| --- | --- |
| `0` | The check completed and no new finding crossed `--fail-on`. |
| `1` | A new finding crossed `--fail-on`. Fix it. |
| `2` | No new finding crossed `--fail-on`, but some check did not complete; or the CLI or service failed. |
| `3` | The check was still running when `--timeout` ran out. Run the same check again. |

The service sets the status and the CLI follows it. A failed request prints no
report on stdout, so exit `2` with an empty stdout is a failure, never a clean
result. Exit `2` with a report means the check finished: its `incomplete` list
names the checks that did not complete, and running it again usually gives the
same answer. Inconclusive checks about files the change does not write are listed in
`inconclusiveElsewhere` and never cause exit `2`. A pending check prints
`{"status": "pending", ...}` in `json`;
running the same check again, in any format, picks up the same analysis.

Failures print the service's message, remedy, and install or top-up link verbatim. What
the usual codes mean:

| Code | What to do |
| --- | --- |
| `auth_required`, `auth_expired` | Sign in again with `slopcop login`. |
| `device_flow_disabled` | This deployment has no browser login; use `slopcop login --token`. |
| `repo_not_indexed` | This login cannot check the repository. A private one needs the SlopCop GitHub App installed with access to it, then `slopcop login` again; otherwise fix the remote URL. |
| `no_access` | The account needs access to the repository. |
| `base_not_found` | The service's clone lacks the base commit; pass one it has with `--base`. |
| `payload_too_large` | The change is over the service's limit; check a smaller change. |
| `finding_not_found` | `slopcop report` named a finding this login cannot see; copy the id from the latest check. |
| `invalid_report` | The service rejected the report; give a reason of 1 to 2000 characters. |
| `insufficient_credits` | The workspace is out of credits and its grace period has ended; only its free repository can be checked. Tell the human: a workspace owner adds credits at the printed link. Do not retry. |
| `rate_limited` | Wait, then retry. |
| `service_unavailable` | Retry; the service is unavailable. |

## Act on the findings

The new findings are the work: fix them in the working tree and run the same
check again. The loop ends when the check reports no new findings: exit `0`, or
exit `2` with a report whose `summary.newFindings` is `0` (say which checks did
not complete). A finding you reported as a false positive comes back dismissed
on the next check, not new, so it no longer keeps the loop going; tell the
human which findings you reported and why. Exit `3` means wait and run it
again; it is not a result. Do not
fix pre-existing findings unless asked: they are not this change's, and they
never stop the loop. A repository rule is a `.slopcop/policies/*.rqlp` file that travels with
the change, so a new rule is picked up by the next check with no extra setup.

## Report a false positive

A finding can be wrong for the code it flags. When you are confident it is,
report it by its id from the check's report, rather than bend correct code to
satisfy the rule:

```console
slopcop report 6f1c2a9e-3b4d-4e5f-8a7b-0c1d2e3f4a5b \
  --reason "parse() returns early on empty input (line 12), so the flagged index on line 30 is never out of range"
```

- Report only when you are confident the finding is wrong for this code. Never
  report one because it is inconvenient, hard to fix, or in the way of
  finishing.
- Give a specific reason that cites the code: why the flagged path is
  unreachable, or where the input is already sanitized. "False positive" or
  "not a real issue" is not a reason.
- Every report and its reason is visible to the team on the SlopCop dashboard.
  An owner can reinstate the finding, and false-positive rates are tracked per
  rule.
- A reported finding stops failing checks for this repository: the next check
  lists it as dismissed, so the check-and-fix loop can end.
- Report only new findings from the change you are checking. Do not report
  pre-existing findings you were not asked about.
- When unsure, fix the code or ask the human instead.

`slopcop report` exits `0` when the report is recorded or the finding was
already reported, and `2` on any failure, with the service's message and
remedy on stderr. `--format json` prints the service's answer, whose `status`
is `recorded` or `already_reported`. It signs in and finds the service the
same way a check does.
