---
name: slopcop
description: Check a working-tree change with the slopcop CLI, which sends the change to the hosted SlopCop check API. Use when asked to run slopcop, to check a change for policy findings, to fix what a check reported, or to gate a change in CI.
---

# SlopCop

`slopcop` checks a change through the hosted check API. The CLI sends one git
patch from the base commit to the working tree; the service applies it, runs
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
when nothing changed: an empty patch is a real check, not a skip.

Flags worth knowing: `--format human|json|sarif` (default `human`),
`--fail-on never|finding|note|warning|error` (default `finding`, so any new
finding fails the check; the service alone defaults to `warning`), `--policy
PATH` for a repository-relative `.rqlp` file (repeatable), and
`--no-policy-discovery` to drop the `.slopcop/policies/**/*.rqlp` rules that
otherwise join automatically. `--show-existing` also lists the findings that
were already in the code. `--timeout DURATION` bounds the whole wait for a
result (default 10 minutes) and `--verbose` prints the resolved request to
stderr.

## New and pre-existing findings

A check fails only on findings the change introduced or touched. Findings that
were already in the code are counted in `summary.existingFindings`, never
change the exit code, and are listed only with `--show-existing`. In `json`,
each listed finding has `new`; `summary.newFindings` counts the new ones. In
`sarif`, `baselineState` is `new` or `unchanged`. Each finding also carries its
`id`: an `ID:` line in `human`, `id` in `json`, and
`properties["slopcop.findingId"]` in `sarif`.

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
| `repo_not_indexed` | Analyze the repository on SlopCop first; a private one needs the SlopCop GitHub App. |
| `no_access` | The account needs access to the repository. |
| `base_not_found` | The service's clone lacks the base commit; pass one it has with `--base`. |
| `payload_too_large` | The change is over the service's limit; check a smaller change. |
| `finding_not_found` | `slopcop ignore` got an id that is not from a check run with this login; use one a check printed. |
| `insufficient_credits` | The workspace is out of credits, so no check runs. Tell the user; they add credits at the printed link. |
| `service_unavailable` | Retry; the service is unavailable. |

## What a result claims

A report answers two separate questions. Is each listed finding real? That is
its `certainty`. Did anything go unchecked? That is `status`, `incomplete`,
and `failure`. An incomplete check does not make the findings it reported
doubtful, and a definite finding does not make the check complete. Never read
"no findings" as "nothing wrong" unless the check also completed.

Status:

| Term | Meaning |
| --- | --- |
| `clean` | Every selected check completed for the files the change writes, and no new finding crossed `--fail-on`. The only status that supports "nothing found", and only for those checks, files, and lines. |
| `findings` | A new finding crossed `--fail-on`. Other checks may still be incomplete. |
| `incomplete` | No new finding crossed `--fail-on`, but some check could not answer for the changed files, or the analysis failed. Neither a pass nor a fault in the code. |
| `pending` | Not a result yet. Run the same check again. |

Finding `certainty`:

| Term | Meaning |
| --- | --- |
| `definite` | Bifrost proved the pattern or relation holds at this location. Fix it. |
| `possible` | The match depends on something Bifrost could not settle, such as an ambiguous receiver or dispatch, name-based resolution, several candidate declarations, or a "may" relation. It can be a false positive: read the code before changing it, and tell the user when it is wrong. |
| `heuristic` | A text-pattern match, such as a secret-like string. Confirm it the same way. |

Why a check is `incomplete` (`incomplete[].reason`):

| Term | Meaning | What to do |
| --- | --- | --- |
| `inconclusive` | The check ran but could not prove there is nothing more to find. The usual cause is code or a file the analysis cannot fully model, such as an unsupported construct or language, or a size budget it ran out of. It can also mean Bifrost judged the run unreliable. The findings it did report keep their certainty. | Report the policy ids. Rerunning gives the same answer, so do not loop on it, and do not rewrite working code only to clear it. |
| `timed_out` | The check ran out of time and has no verdict. | Run it again once. A smaller change may finish. |
| `base_comparison_failed` | The change could not be compared with its base commit, so findings that were already there may be reported as new. Here the risk is extra findings, not missed ones. | Check that the change really introduced each finding before fixing it. |
| `other` | It did not complete for a reason the service does not classify. | Report it. |

Other bounds in the report:

- `inconclusiveElsewhere`: inconclusive checks about files the change does not
  write. They never change the status. Mention them, but they are not this
  change's.
- `summary.omittedFindings`: a check found more than it may report, and only
  the most severe are listed. The list is not everything; fix those and run
  again to see the rest.
- `summary.existingFindings`: findings already in the code, counted but not
  listed (see above).
- `failure`: the analysis as a whole did not complete, and the report makes no
  claim about the code, even if it lists findings. `applied` is `false`
  whenever the analysis did not complete.

## Act on the findings

The new findings are the work: fix them in the working tree and run the same
check again. The loop ends when the check reports no new findings: exit `0`, or
exit `2` with a report whose `summary.newFindings` is `0` (say which checks did
not complete, and why, using the terms above). Read a `possible` or
`heuristic` finding before fixing it, since it may be a false positive. Exit
`3` means wait and run it again; it is not a result. Do not
fix pre-existing findings unless asked: they are not this change's, and they
never stop the loop.

When a finding is a false positive, run `slopcop ignore <id>` with its id.
That finding is not reported again for this repository.

A repository rule is a `.slopcop/policies/*.rqlp` file that travels with
the change, so a new rule is picked up by the next check with no extra setup.
