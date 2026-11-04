---
title: Run a check
description: "What slopcop sends, how the base commit is chosen, the flags that shape a check, and what happens when the patch is empty."
sidebar:
  order: 1
---

```console
slopcop                          # check the change in the current checkout
slopcop check --if-enabled       # automatic check: skip a repository not enabled here
slopcop check --format json      # same, machine-readable
slopcop --root ../repo --base origin/main
```

Running `slopcop` with no subcommand runs the check; `slopcop check` is the
same command. Every check it sends is one request, and a clean result or a
failure always comes from the service. `--if-enabled` can skip a repository
the workspace has not enabled; that is a `skipped` result, never a clean one.

## What is sent

One git patch from the base commit to the working tree. The patch carries
committed but unpushed work, unstaged edits, untracked files, deletions,
renames, mode changes, and binary content. Git-ignored files stay out, except
selected `.slopcop/policies` rules, which must travel because the service
evaluates them from the patched checkout.

An empty change is sent as an empty patch, not filtered out. The service owns
the limits on what a check accepts: when a change is too large or touches too
many files, its `payload_too_large` response is printed verbatim, and the CLI
imposes no limits of its own.

With the patch go the repository's URL (canonicalized from the `origin`
remote), the base commit's full hash, the policy selection, and the `--fail-on`
level. Run from a Claude Code or Codex session, a check also sends which models
wrote each changed file. [What leaves your machine](/account/privacy/) lists
every field.

## The base commit

The base is the commit the service applies the patch to. It must already be in
the service's clone, which is why it should be a commit origin has. By default
the CLI resolves it in this order:

1. the merge base with the branch's upstream;
2. when that gives an empty patch, as on a branch whose commits are all pushed,
   the merge base with `origin/HEAD`, so a check right after a push still
   covers the branch;
3. `HEAD`, when neither is known.

On the default branch with nothing unpushed, the base is `HEAD` and the patch
is empty.

### Choose the base yourself

| Flag | Meaning |
| --- | --- |
| `--base REV` | The branch or revision the change started from. Like `git diff REV...HEAD`, the base is the merge base of `REV` and `HEAD`, so commits that landed on `REV` after the branch point are not sent as reverse edits. Stops with an error when `REV` shares no history with `HEAD`. |
| `--base-exact REV` | That exact commit, without a merge base. A target branch that moved on then shows up as reverse edits. |

The two cannot be combined. When you check work you have already committed,
pass `--base` with the branch the change targets, such as the pull request's
base:

```console
slopcop --base origin/main
```

A base the service's clone lacks comes back as `base_not_found` with its
remedy. The CLI then asks origin for its default branch (`git ls-remote`, no
prompt, ten-second limit) and adds a line when the clone explains the failure:
origin's default branch was renamed and `origin/HEAD` still names the old one,
or the base is not on origin's default branch. Follow that line.

## An empty patch

An empty patch is a real check, not a skip: one request goes out and the
service answers. The report then has `"emptyChange": true` (`sarif`:
`properties["slopcop.emptyChange"]`), and stderr says so and suggests `--base`.
A `clean` status on an empty change means nothing was checked, not that the
work passed.

## Flags

| Flag | Meaning |
| --- | --- |
| `--root PATH` | repository to check (default `.`) |
| `--base REV` | branch or revision the change started from; the base is its merge base with `HEAD` |
| `--base-exact REV` | exact base commit or revision, without a merge base |
| `--format human\|json\|sarif` | report format (default `human`); see [Output formats](/checks/output-formats/) |
| `--fail-on never\|finding\|note\|warning\|error` | exit-1 threshold for new findings (default `finding`) |
| `--policy PATH` | repository-relative `.rqlp` file to evaluate (repeatable); see [Repository rules](/checks/repository-rules/) |
| `--no-policy-discovery` | skip `.slopcop/policies` rules |
| `--timeout DURATION` | how long to wait for the result (default `10m`); a check still running then exits `3` |
| `-w`, `--workspace NAME\|ID` | check in, and bill, this workspace of the account instead of the login's; see [Workspaces](/account/workspaces/) |
| `--if-enabled` | check only when the repository is active in the workspace; otherwise exit `0` and send nothing. For automatic checks; see [Repositories](/account/repositories/) |
| `--add-repository` | add the repository to the workspace `--workspace` names, which then pays for it, without asking at a terminal; for after the user approved it. A check without it never adds a repository. See [Repositories](/account/repositories/) |
| `--service URL` | the check service (also `SLOPCOP_SERVICE`) |
| `--verbose` | print the resolved request to stderr, and name each inconclusive check in `human` |

### The threshold

The CLI always sends `--fail-on`, defaulting to `finding`: a check fails only on
findings the change introduced, so any of them, a note or an unrated one
included, is worth fixing before the change lands. The service's own default
for a request without a level is Bifrost's `warning`; pass `--fail-on warning`
to match it. New findings that cross the threshold exit `1` even when some
rule did not complete.

## Waiting for the result

While the analysis runs, the service answers `check_running` and the CLI waits
the interval it names and asks again, until `--timeout` runs out. It asks by
the check's id, so the change is sent only once, and every ask attaches to the
same running check. If it is still running then, the CLI exits
`3`, prints the service's message and remedy on stderr, and in `json` prints a
`pending` report. Running the same check again picks up the same analysis: an
identical request by the same login within an hour attaches to it rather than
starting a new one.

A check that is interrupted (Ctrl-C, or SIGTERM) prints no report and exits
`130` or `143`. Interrupted before it was sent, nothing reached the service;
after, the service keeps running it, and running the same check again picks up
its result.

## A check of a repository that is not enabled

A check never adds a repository. A check of one the workspace has not added
exits `4` with `repository_not_enabled` and sends nothing. Run `slopcop
enable` in the checkout, at a terminal, to add it and run its first check.
An automatic check can pass `--if-enabled` to exit `0` and send nothing
instead. [Repositories](/account/repositories/) explains adding, the
`--add-repository` flag for after a person approved it, and the active limit.
A private repository needs the workspace's
[GitHub App, GitLab, or Bitbucket connection](/account/private-repositories/)
first.
