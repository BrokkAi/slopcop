---
name: slopcop
description: Check a working-tree change with the slopcop CLI, which sends the change to the hosted SlopCop check API. Use when asked to run slopcop, to check a change for policy findings, to fix what a check reported, or to gate a change in CI.
---

# SlopCop

`slopcop` checks a change through the hosted check API. The CLI sends one git
patch from the base commit to the working tree; the service applies it, runs
the hosted policy catalog plus the repository rules selected, and returns the
report. There is no local analyzer, and the CLI never decides on its own that
there is nothing to check. Run from a Claude Code or Codex session, a check
also sends which models wrote each changed file, read from the session's own
record; `--verbose` prints it.

## Sign in

- `slopcop login` runs the browser device flow.
- `slopcop login --token "$SLOPCOP_TOKEN"` stores an existing credential, which
  is how CI and agents sign in. `$SLOPCOP_TOKEN` alone also works, with no login.
- `--service` or `$SLOPCOP_SERVICE` names another deployment; the default is
  `https://slopcop.com`. `slopcop logout` forgets the stored credential.

## Know which workspace pays

A login runs its checks and ignores in, and bills, one workspace. When it
matters which one, run `slopcop whoami` before a check: it prints the account,
the workspace (name, kind, role), its plan (Pro or Free) and the service URL
(`--format json` for `service`, `account`, `workspace` and `plan`).

- `slopcop workspace list` lists the account's workspaces; `*` marks this
  login's. `slopcop workspace use <name|id>` moves this login to another one,
  and with it every agent using the login, so it asks the person at a
  terminal and refuses when stdin is not one. Do not try to answer it.
- To check, ignore or list repositories in another workspace, pass
  `-w`/`--workspace <name|id>` to that command (or set `$SLOPCOP_WORKSPACE`).
  It applies to that command only and moves no one.
- `slopcop repo list` shows the workspace's repositories as `active`,
  `disabled`, or `enabled, over the limit`. Every active repository after the
  first costs credits every 30 days, and a disabled one is neither checked nor
  charged. A check of a disabled repository answers `repository_disabled`.
- Do not choose a workspace the user did not name: it changes which
  workspace pays.
- A first check adds the repository to the workspace. When the workspace would
  then pay for it, the check exits `4` with `repository_add_unconfirmed`, and
  stderr names the workspace it would add to. Stop and tell the user: the
  repository, the workspace, and the cost from the message. Only when the
  user has approved adding it to that workspace, run the check again with
  `--add-repository --workspace <name|id>`, naming the workspace they
  approved; the flag refuses to run without `--workspace`. Do not pass it
  without that approval, pick a workspace they did not name, or pipe an
  answer into the check. Without the flag, a check that is not at a terminal
  asks nothing and exits `4` the same way.

## Run a check

```console
slopcop                        # check the current checkout
slopcop check --format json    # machine-readable report
slopcop --root ../repo --base HEAD~1
```

The base commit defaults to the merge base with the branch upstream, then the
merge base with `origin/HEAD`, then `HEAD` when neither is known. If the
upstream merge base gives an empty patch, as on a pushed branch, the CLI uses
the `origin/HEAD` merge base instead. One request goes out per run, even when
nothing changed: an empty patch is a real check, not a skip. Its report has
`"emptyChange": true` and stderr says so; a `clean` status then means nothing
was checked, not that the work passed.

When you check work you have already committed, pass `--base` with the branch
the change targets, such as the PR's base (`--base origin/main`), instead of
relying on the default. On the default branch with nothing unpushed, the
default base is `HEAD` and the patch is empty.

Flags worth knowing: `--format human|json|sarif` (default `human`),
`--fail-on never|finding|note|warning|error` (default `finding`, so any new
finding fails the check; the service alone defaults to `warning`), `--policy
PATH` for a repository-relative `.rqlp` file (repeatable), and
`--no-policy-discovery` to drop the `.slopcop/policies/**/*.rqlp` rules that
otherwise join automatically. `--timeout DURATION` bounds the whole wait for a
result (default 10 minutes) and `--verbose` prints the resolved request to
stderr. `--add-repository`, with `--workspace`, confirms adding the
repository once the user has approved it (see above).

When the service reports a newer release, the CLI prints one line on stderr,
at most once a day, starting `slopcop vX.Y.Z is available`. It never touches
stdout or the exit code; mention it to the user rather than upgrading on your
own. `SLOPCOP_NO_UPDATE_NOTICE=1` turns it off.

In Claude Code and Codex, `slopcop init` can install a `Stop` hook that checks
the change when you stop and hands back new findings in files you wrote. It does
not send a check whose result your session already has, so a check you ran by
hand is not repeated. Keep running checks as described above; the hook is a
backstop, not a replacement.

## New and pre-existing findings

A check fails only on findings the change introduced or touched. Findings that
were already in the code are counted in `summary.existingFindings`, never
change the exit code, and are not listed. In `json`, each listed finding has
`new`; `summary.newFindings` counts them. In `sarif`, `baselineState` is `new`.
Each finding also carries its
`id`: an `ID:` line in `human`, `id` in `json`, and
`properties["slopcop.findingId"]` in `sarif`.

## Read the exit code

| Exit | Meaning |
| --- | --- |
| `0` | The check completed and no new finding crossed `--fail-on`. |
| `1` | A new finding crossed `--fail-on`. Fix it. |
| `2` | No new finding crossed `--fail-on`, but some check did not complete; or the CLI or service failed. |
| `3` | The check was still running when `--timeout` ran out. Run the same check again. |
| `4` | The service refused the check, and only a person can clear it. Do not retry; tell the user the `code` and remedy. |
| `130`, `143` | The check was interrupted (Ctrl-C or SIGTERM) before it answered. It printed no report; run the same check again. |

The service sets the status and the CLI follows it. A failed request prints
`{"status": "failed", "code": ..., "message": ..., "remedy": ..., "retryable":
...}` in `json` (in `human`, nothing on stdout, and stderr starts with
`slopcop: <code>:`). Act on its `code` with the table below: never treat it as
a clean result, and retry only when `retryable` is `true`. It exits `4` when
only a person can clear the refusal (signing in, credits, access, or what the
workspace checks), and `2` for the rest. Any other error
from the CLI exits `2` with an empty stdout. Exit `2` with a check report
means the check finished: its `incomplete` list
names the checks that did not complete, and running it again usually gives the
same answer. Inconclusive checks never cause exit `2` (see below). A pending check prints
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
| `insufficient_credits` | The workspace is out of credits, so no new check runs. Tell the user; they add credits at the printed link. |
| `repository_disabled` | The workspace's owner disabled the repository. Run `slopcop whoami` to name that workspace, and tell the user. |
| `workspace_not_found` | Pick a name from `slopcop workspace list`. |
| `repository_add_unconfirmed` | The check would add a repository the workspace then pays for. Tell the user which workspace and what it costs. Once they approve it, run the check again with `--add-repository --workspace <name|id>` for that workspace; otherwise they confirm at a terminal or on the Repositories page. |
| `client_outdated` | This slopcop is older than the service accepts. Upgrade it (the install channel's upgrade command), then run the check again. |
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
| `clean` | Every selected check completed for the files the change writes (an inconclusive one counts as completed), and no new finding crossed `--fail-on`. The only status that supports "nothing found", and only for those checks, files, and lines. |
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
| `unreliable` | Bifrost judged the run unreliable, for example evaluated with a scope file it could not load. The findings it did report keep their certainty. A service from before this code says `inconclusive`. | Report the policy ids. Rerunning gives the same answer, so do not loop on it, and do not rewrite working code only to clear it. |
| `timed_out` | The check ran out of time and has no verdict. | Run it again once. A smaller change may finish. |
| `base_comparison_failed` | The change could not be compared with its base commit, so findings that were already there may be reported as new. Here the risk is extra findings, not missed ones. | Check that the change really introduced each finding before fixing it. |
| `other` | It did not complete for a reason the service does not classify. | Report it. |

Other bounds in the report:

- `summary.inconclusive`: checks that ran but could not prove there is
  nothing more to find, typically at a library boundary or in code the
  analysis cannot fully model. Complete proof is rare in static analysis, so
  this is normal and is not a problem with the change. They never change the
  status. Do nothing about them: do not report them as gaps, rerun, rewrite
  code, or ignore or disable a policy because of them.
- `summary.omittedFindings`: new findings left out because one check found
  more than it may report; the most severe are listed. Give them little
  weight: they never change the status, and a large count is almost always
  one noisy policy over a large new file. Mention the count, but do not
  rerun just to see the rest. Findings already in the code that were left
  out are counted in `existingFindings`, not here.
- `summary.existingFindings`: findings already in the code, counted but not
  listed (see above).
- `failure`: the analysis as a whole did not complete, and the report makes no
  claim about the code, even if it lists findings. `applied` is `false`
  whenever the analysis did not complete.
- `emptyChange`: `true` only when the patch was empty because the working tree
  matches the base. Nothing was checked; rerun with `--base` if you meant to
  check committed work.

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
