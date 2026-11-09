---
name: slopcop
description: Check a working-tree change with the slopcop CLI, which runs the hosted SlopCop static check and a local review by independent LLM reviewers. Use before git push or gh pr create, when the user asks to run slopcop or fix what it reported, or when a change is ready to check.
---

# SlopCop

By default, `slopcop` checks a change through the hosted check API and local
independent LLM review concurrently. For the static half, the CLI sends one git
patch from the base commit to the working tree; the service applies it, runs the
hosted policy catalog plus the repository rules selected, and returns the
report. The hosted static analyzer needs no per-language install. `--mode ai`
runs local review without the service, login, enabled repository, or supported
origin. `--if-enabled` skips only the static half for an inactive repository;
local review still runs in `both` mode. Run from a Claude Code or Codex session,
a check also sends which models wrote each changed file, read from the session's
own record; `--verbose` prints it.

## Hosted static check and repository enablement

The hosted static half checks only repositories enabled in the workspace.
Local review in `ai` mode does not require enablement. `slopcop status`
says whether this checkout is added and active. `slopcop enable` adds it, and
only a person at a terminal can run it; it shows the workspace and what adding
the repository costs.

Before an automatic or routine check, run `slopcop check --if-enabled`. The
static half sends nothing when the repository is inactive. In `both` mode the
local review still runs; inspect `review` and the merged exit. In `static` mode
the result is `"status": "skipped"` and exit `0`. When a check says the
repository is not active, do nothing: do not run `slopcop enable`, do not ask
the user to add it, and do not describe the change as checked. Mention it only
when the user asks about SlopCop.

When the user asks for a hosted static check of a repository that is not
enabled, tell them to run `slopcop enable` at a terminal. `--mode ai` can
still run the local review. Never run `slopcop enable` yourself, and never pass
`--add-repository` without the user's explicit approval for the workspace they
named.

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
`slopcop status` shows the same for this checkout: the repository `origin`
names, whether the workspace has it and whether it is active.

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
- A check never adds a repository. The only ways a repository joins a
  workspace are `slopcop enable`, run by a person at a terminal, or
  `check --add-repository --workspace <name|id>` after a person approved
  adding it to that workspace. `slopcop enable` runs the first check and says
  `added owner/name to "Workspace"` on stderr; `json` carries
  `"repositoryAdded": true`.
- A plain check of a repository the workspace has not added exits `4` with
  `repository_not_enabled` and sends nothing. Tell the user to run `slopcop
  enable` at a terminal when they asked for a check; otherwise continue
  without checking, as `--if-enabled` says.
- Do not pass `--add-repository` without the user's explicit approval, do not
  pick a workspace they did not name, and do not pipe an answer into a
  command that asks at a terminal.

## Modes and reviewer requirements

Where reviewers are configured, a check runs the hosted static half and local
review concurrently (`both` mode). With none configured, or in CI, a check with
no mode set runs only the static half and, outside CI, says so on stderr.
`--mode static` or `--static` runs only the hosted half; `--mode ai` or `--ai`
runs only review. `--if-enabled` skips only an inactive static half. The Stop
hook and `slopcop enable` remain static-only. A single captured snapshot
includes committed, uncommitted, and untracked work, excluding ignored
files. Reviewers read `git diff <baseTree> <snapshotTree>` themselves.

Reviewers, in priority order, are GPT Terra (`terra`) and GPT Sol (`sol`) through
Codex ACP, DeepSeek Flash (`flash`) through dsh ACP, and Claude Haiku (`haiku`)
through Claude ACP. Each uses the newest model of its family at high effort.
Every reviewer must reply exactly `No findings.` for a clean review. `slopcop
init` probes each family once when reviewers are not configured and the mode is
not static, then saves the first two that work (or one). Probe prompts use your
logins. Already configured reviewers are left alone; unset `review.reviewers`
and run init to pick again. `slopcop init --no-reviewers` skips probing.
A check runs exactly the configured reviewers. It never reorders or replaces
them. If one cannot run, review fails with exit `2` and names the reviewer and
reason. With no reviewers configured, run `slopcop init` or set
`review.reviewers`. Do not change reviewers or adapters to get past a failure;
report it to the user. The prompt carries the captured diff. Reviewers read repository content,
which is untrusted. Claude and DeepSeek reviewers may only read and search
files; Slopcop refuses their requests to run commands or change files. Codex
reviewers run in Codex's read-only sandbox.

For intent, the CLI reads the user's own messages from the Claude Code
transcript or Codex rollout when found, then uses `--intent TEXT`, else
labels it `not available`. Give the actual user request, not your account
of the implementation. The prompt and diff go to reviewer model providers
through your harness logins; review data does not go to SlopCop. Use
`--mode static` to avoid that review.

In Claude Code, run Bash with **`run_in_background: true`** and poll
TaskOutput until the check exits. In Codex, run in the foreground and poll
the running shell session. Do not detach with `nohup` or `&`. Reviews can
take minutes; a running command is not a pass. `--review-timeout` defaults
to `20m`. A complete review is reused when its inputs have not changed.

## Run a check

```console
slopcop                            # check the current checkout
slopcop check --if-enabled         # automatic check: skip an inactive repository
slopcop check --format json        # machine-readable report
slopcop --root ../repo --base origin/main
```

The base commit defaults to the merge base with the branch upstream, then the
merge base with `origin/HEAD`, then `HEAD` when neither is known. If the
upstream merge base gives an empty patch, as on a pushed branch, the CLI uses
the `origin/HEAD` merge base instead. In modes with static analysis, one request
goes out even when nothing changed: an empty patch is a real check, not a skip.
Its report has `"emptyChange": true` and stderr says so; a `clean` status then
means nothing was checked, not that the work passed. `--if-enabled` is the one
skip: it sends nothing when the repository is not active.

When you check work you have already committed, pass `--base` with the branch
the change targets, such as the PR's base (`--base origin/main`), instead of
relying on the default. `--base REV` works like `git diff REV...HEAD`: the base
is the merge base of `REV` and `HEAD`, so the patch covers only the branch's
work even after the target branch has moved on. A branch stacked on another
PR's branch that merged a newer `origin/HEAD` also sends only its own work once
the prerequisite has landed; if it has not, stderr warns that the patch carries
the merged-in commits. `--base-exact REV` diffs
against that exact commit instead; a target branch that moved on then shows up
as reverse edits. The two flags cannot be combined, and `--base` stops with an
error when `REV` shares no history with `HEAD`. On the default branch with nothing unpushed, the
default base is `HEAD` and the patch is empty.

Flags worth knowing: `--format human|json|sarif` (default `human`), `--fail-on
never|finding|note|warning|error` (default `finding`, so any new finding fails
the check; the service alone defaults to `warning`), `--policy PATH` for a
repository-relative `.rqlp` file (repeatable), and `--no-policy-discovery` to
drop the `.slopcop/policies/**/*.rqlp` rules that otherwise join automatically.
`--timeout DURATION` bounds the whole wait for a result (default 10 minutes) and
`--verbose` prints the resolved request to stderr. `--if-enabled` is the
automatic path for the static half: it sends nothing when the repository is
inactive, but local review still runs. `--add-repository`, with `--workspace`,
adds the repository once the user has approved it (see above); prefer `slopcop
enable`, which asks the person directly.

When the service reports a newer release, the CLI prints one line on stderr,
at most once a day, starting `slopcop vX.Y.Z is available`. It never touches
stdout or the exit code; mention it to the user rather than upgrading on your
own. `SLOPCOP_NO_UPDATE_NOTICE=1` turns it off.

In Claude Code and Codex, `slopcop init` can install a `Stop` hook that checks
the change when you stop and hands back new findings in files you wrote. It
sends nothing for a repository the workspace does not have active, so it never
adds one. A dependency manifest or lockfile a package manager changed, such as
after `npm install` or `cargo add`, counts as one you wrote. It does not send a
check whose result your session already has, so a check you ran by hand is not
repeated. Keep running checks as described above; the hook is a backstop, not
a replacement.

## New and pre-existing findings

The static half fails only on findings the change introduced or touched.
Review findings are model judgments about the captured change. Findings that
were already in the code are counted in `summary.existingFindings`, never
change the exit code, and are not listed. In `json`, each listed finding has
`new`; `summary.newFindings` counts them. In `sarif`, `baselineState` is `new`.
Each finding also carries its
`id`: an `ID:` line in `human`, `id` in `json`, and
`properties["slopcop.findingId"]` in `sarif`.

## Read the exit code

| Exit | Meaning |
| --- | --- |
| `0` | Every half that ran completed and nothing crossed `--fail-on`. The static check is clean, or skipped by `--if-enabled`; the review is clean, or had nothing to review. Check `status` for `skipped` and `review.status` for `empty` before you call the change checked. |
| `1` | A new static finding crossed `--fail-on`, or a reviewer reported findings (unless `--fail-on never`). Fix the real ones and run the check again. |
| `2` | Nothing crossed `--fail-on`, but a static check did not complete, the review failed (a reviewer failed, timed out or replied empty, or too few reviewers were available), or the CLI or service failed. Read the errors and log paths. |
| `3` | The static check was still running when `--timeout` ran out. Run the same check again; a complete review of the same content is reused. |
| `4` | The service refused the static check, and only a person can clear it. Do not retry; tell the user the `code` and remedy. |
| `130`, `143` | The check was interrupted (Ctrl-C or SIGTERM) before it answered, and the reviewers were stopped. It printed no report; run the same check again. |

The CLI prioritizes interrupt, findings, a static refusal needing a person,
failure or incompleteness, static pending, then clean. JSON `status` matches
the exit code; `staticStatus` keeps a different service status. Read
`review.status`, `review.reviewers`, `review.error`,
and `review.logDir`. Prompt and reviewer logs are under `<git-dir>/slopcop/`.

The static service sets its own status; the CLI merges it with review. A failed
request prints `{"status": "failed", "code": ..., "message": ..., "remedy": ...,
"retryable": ...}` in `json` (in `human`, nothing on stdout, and stderr starts
with `slopcop: <code>:`). Act on its `code` with the table below: never treat it
as a clean result, and retry only when `retryable` is `true`. It exits `4` when
only a person can clear the refusal (signing in, credits, access, or what the
workspace checks), and `2` for the rest. Any other error from the CLI exits `2`
with an empty stdout. Exit `2` with a check report means the check finished: its
`incomplete` list names the checks that did not complete, and running it again
usually gives the same answer. Inconclusive checks never cause exit `2` (see
below). A pending check prints `{"status": "pending", ...}` in `json`; running
the same check again, in any format, picks up the same analysis.

Failures print the service's message, remedy, and install or top-up link verbatim. What
the usual codes mean:

| Code | What to do |
| --- | --- |
| `auth_required`, `auth_expired` | Sign in again with `slopcop login`. |
| `device_flow_disabled` | This deployment has no browser login; use `slopcop login --token`. |
| `repo_not_indexed` | Analyze the repository on SlopCop first; a private one needs the SlopCop GitHub App. |
| `no_access` | The account needs access to the repository. |
| `base_not_found` | The service's clone lacks the base commit; pass one it has with `--base`. When the clone can tell why, a line after the remedy says so: origin's default branch was renamed and `origin/HEAD` still names the old one, or the base is not on origin's default branch. Follow that line. |
| `payload_too_large` | The change is over the service's limit; check a smaller change. |
| `finding_not_found` | `slopcop ignore` or `unignore` got an id that is not from a check run with this login; use one a check printed. |
| `insufficient_credits` | The workspace is out of credits, so no new check runs. Tell the user; they add credits at the printed link. |
| `repository_disabled` | The workspace's owner disabled the repository. Run `slopcop whoami` to name that workspace, and tell the user. |
| `repository_not_enabled` | The repository is not added to this workspace, so nothing was checked. When the user asked for the check, tell them to run `slopcop enable` at a terminal; when the check was automatic, continue without mentioning it. |
| `workspace_not_found` | Pick a name from `slopcop workspace list`. |
| `repository_add_unconfirmed` | The check tried to add a repository the workspace then pays for. Do not retry with `--add-repository` unless the user approved it for the workspace named; otherwise tell them to run `slopcop enable` at a terminal. |
| `client_outdated` | This slopcop is older than the service accepts. Upgrade it (the install channel's upgrade command), then run the check again. |
| `service_unavailable` | Retry; the service is unavailable. |

## What a result claims

A report answers two separate questions. Is each listed finding real? That is
its `certainty`. Did anything go unchecked? That is `status`, `incomplete`,
and `failure`. An incomplete check does not make the findings it reported
doubtful, and a definite finding does not make the check complete. Never read
"no findings" as "nothing wrong" unless the check also completed.

The following terms describe static rules; the merged JSON `status` also
accounts for the local review.

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
| `unreliable` | Bifrost judged the run unreliable, for example evaluated with a scope file it could not load. `detail`, when present, names the cause, such as `type=incomplete reasons=analyzer_process_failed exit_code=137`. The findings it did report keep their certainty. A service from before this code says `inconclusive`. | Report the policy ids. Rerunning gives the same answer, so do not loop on it, and do not rewrite working code only to clear it. |
| `timed_out` | The check ran out of time and has no verdict. | Run it again once. A smaller change may finish. |
| `base_comparison_failed` | The change could not be compared with its base commit, so findings that were already there may be reported as new. Here the risk is extra findings, not missed ones. | Check that the change really introduced each finding before fixing it. |
| `unsupported` | Bifrost could not evaluate the rule here because the analysis it needs is not available for this code. `detail` names what it lacked, such as `type=query_domain domain=execution_scope capability=seed_scope reason=query_source_not_scope_aware`. A service from before this code says `other`. | Report the policy id with its `detail`. Rerunning gives the same answer, so do not loop on it, and do not rewrite working code only to clear it. |
| `other` | It did not complete for a reason the service does not classify. `detail`, when present, gives Bifrost's completion type and reason codes, such as `type=failed reasons=source_read_failed`. | Report it, with its `detail`. |

`incomplete` names at most fifty checks; `summary.incompletePolicies` counts
them all, and `incompleteUnlisted` says how many are not named when the list
was cut. `incompleteReasons` groups the named ones by reason and detail, most
common first. When most share one reason, report that reason once with the
count instead of listing every policy id. `runFailures` names each cause that
failed a whole analyzer run once, with `policies` counting every check it left
incomplete: report each one once with its `detail`, as a fault in the run, not
the change, and do not rewrite code to clear it.

Other bounds in the report:

- `summary.inconclusive`: checks that ran but could not prove there is
  nothing more to find, typically at a library boundary or in code the
  analysis cannot fully model. Complete proof is rare in static analysis, so
  this is normal and is not a problem with the change. They never change the
  status. `inconclusive` names them by policy id, with `elsewhere: true` for
  one that concerns only files the change does not write. Do nothing about
  them: do not report them as gaps, rerun, rewrite code, or ignore or disable
  a policy because of them. Name them only when someone asks how much a
  `clean` result covers.
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
- `skillUpdated`: present only when this run installed or replaced this
  skill. Each entry names the skill `dir`, the version it replaced (`from`),
  the new one (`to`) and the `changed` files. Your loaded copy of these
  instructions is out of date: reread `SKILL.md` in that directory before you
  act on the report. A `backup` is where a copy of a skill with local edits
  went; tell the user. The failed and pending reports carry it too, and stderr
  says the same in a `slopcop: updated the slopcop skill` line.

## Act on the findings

The new findings are the work: fix them in the working tree and run the same
check again. A static finding traces to a rule; a review finding is a model's
judgment, so read the code it names before you change anything, and use
`slopcop override` (below) when it is wrong. The loop ends when the check
approves the tree: `approved` is `true` in `json`, and where the push gate is
installed, stderr says the tree may be pushed. That needs a clean review and
no new static finding at or above the push level (`error` by default); a
static check that ended `incomplete` does not prevent it, unless it lists
`runFailures`: then the analyzer run failed and checked nothing, so run the
check again. New static findings
below the push level, warnings and notes by default, are advisory: they do not
block the push, though they still exit `1` under the default `--fail-on`. Fix
the real ones in this change, and tell the user about any you leave. A failed
review, a service failure, or exit `3` never approves the tree. Read a
`possible` or `heuristic` finding before fixing it, since it may be a false
positive. Exit `3` means wait and run it again; it is not a result. Do not fix
pre-existing findings unless asked: they are not this change's, and they never
stop the loop.

When a hosted static finding is a false positive, run `slopcop ignore <id>`
with its id.
That finding is not reported again for this repository. Undo a mistaken ignore
with `slopcop unignore <id>`. List the repository's ignored findings, with
their ids, with `slopcop ignored`. A finding marked as matching an ignored
finding (`previouslyIgnored`) is one you ignored before its code changed: if it
is still a false positive, ignore its new id, and tell the user.

A repository rule is a `.slopcop/policies/*.rqlp` file that travels with
the change, so a new rule is picked up by the next check with no extra setup.

## Override, gate, and configuration

For demonstrably incorrect or unreasonable **complete** review findings,
run `slopcop override --reason "concrete explanation"`. This accepts the
last reviewed snapshot only when the static half has no new findings at or
above the push level and completed, was skipped, or did not run.
Never override a real bug, timeout, failed or incomplete review, or a
different tree. Do not switch reviewers or reduce the count to bypass a
failure.

Set the push level with `gate.blockOn` in the config file. Only a person
should change it. The default is `error`; `--fail-on` affects exit codes,
not push approval.

A passing check (see above) or valid override records
`refs/slopcop/reviewed/<tree>`. A later check that does not approve the same
nonempty tree removes that ref, including an override. Rerun `slopcop override`
if the review findings are unchanged.
The push gate, in Claude Code, Codex, and Grok, blocks `git push`,
`gh pr create`, and GitHub MCP tools that push files or open a pull request
until HEAD has exactly that tree. Never write, read, or delete `refs/slopcop/` yourself; the
gate blocks any command that names it. After checking a dirty tree, commit exactly that state,
including untracked files, before pushing. Gate approval does not authorize a push the user did not request. The
gate checks HEAD in the directory the command runs from and does not follow
`cd` or `git -C`, so do not use them to get around it.

Run `slopcop init` inside each repository to install local skills, static
Stop hooks, and the Claude Code gate. A check updates existing skill
copies but creates none; login does not install them. Set `mode`,
`review.reviewers` (1–4 entries, in order; duplicates are allowed), and
`review.timeout` (default `20m`) with `slopcop config get|set|unset`. Set
reviewers as a comma-separated list. Adapter commands are keyed by adapter at
`review.adapters.<codex|dsh|claude>.command`. In
CI, use `--mode static` unless the runner has Node and harness logins.
