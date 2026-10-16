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
`--fail-on never|finding|note|warning|error` (default `finding`), `--policy
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
`sarif`, `baselineState` is `new` or `unchanged`.

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
same answer. A pending check prints `{"status": "pending", ...}` in `json`;
running the same check again, in any format, picks up the same analysis.

Failures print the service's message, remedy, and install link verbatim. What
the usual codes mean:

| Code | What to do |
| --- | --- |
| `auth_required`, `auth_expired` | Sign in again with `slopcop login`. |
| `device_flow_disabled` | This deployment has no browser login; use `slopcop login --token`. |
| `repo_not_indexed` | Analyze the repository on SlopCop first; a private one needs the SlopCop GitHub App. |
| `no_access` | The account needs access to the repository. |
| `indexing_in_progress` | SlopCop is still indexing; wait, then run the check again. |
| `base_not_found` | The service's clone lacks the base commit; pass one it has with `--base`. |
| `payload_too_large` | The change is over the service's limit; check a smaller change. |
| `service_unavailable` | Retry; the service is unavailable. |

## Act on the findings

The new findings are the work: fix them in the working tree and run the same
check again. The loop ends when the check reports no new findings: exit `0`, or
exit `2` with a report whose `summary.newFindings` is `0` (say which checks did
not complete). Exit `3` means wait and run it again; it is not a result. Do not
fix pre-existing findings unless asked: they are not this change's, and they
never stop the loop. A repository rule is a `.slopcop/policies/*.rqlp` file that travels with
the change, so a new rule is picked up by the next check with no extra setup.
