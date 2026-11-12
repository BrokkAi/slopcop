---
title: Check pull requests
description: "Turn on pull request checks for a GitHub repository and every push gets a SlopCop check run, with each new finding annotated on its line. What it needs, what a pull request shows, and when it is not checked."
sidebar:
  order: 8
---

Turn on pull request checks for a GitHub repository, and every pull request
opened, reopened, or pushed to gets a **SlopCop** check in GitHub. It is the
same answer `slopcop check --base <target branch>` gives on the pull request's
head: the change since the pull request branched, checked against every rule,
with any new finding failing it. Nothing runs in your CI and no token is
stored.

## Turn it on

1. Install the [SlopCop GitHub App](https://github.com/apps/slop-cop) on the
   account or organization that owns the repository, and connect the
   workspace to it from the workspace's settings in the web app. See
   [Private repositories](/account/private-repositories/); a public repository
   needs the App as well, since the App is what posts the check.
2. Accept the App's **Checks** permission. GitHub asks the App's owner once,
   on the installation's settings page. Until then the installation's pull
   requests get no check and nothing else changes; nobody signs in again.
3. In the web app, open the repository's **Settings** and turn on **Check
   pull requests**. A workspace owner or manager can change it; a member sees
   whether it is on.

The next push to a pull request is checked. Pull requests opened before that
are checked on their next push, or when someone re-runs the check.

## What a pull request shows

The check appears in the pull request's checks list as **SlopCop**, and its
**Details** open the full answer:

| Check | Meaning |
| --- | --- |
| Success, "No new findings" | Every rule completed for the files the pull request changes, and it adds no finding. |
| Failure, "N new findings" | The pull request adds findings. Each one is annotated on its line in **Files changed**, with the rule, severity and message, up to 100 per check. |
| Neutral, "Incomplete" | Some rule could not finish for the changed files. Not a pass: the check lists the rules that did not finish. |
| Neutral, "Not checked: …" | SlopCop did not check this push, and says why (below). |

As in a [report](/checks/read-a-report/), only findings on the lines the pull
request added or touched count. Findings already in the code are counted in
the summary and never fail the check.

**Re-run** on the check checks the head again, for example after a top-up or
once a manager has turned the setting back on.

## Block a merge on it

SlopCop never changes branch protection. To keep a pull request with new
findings from merging, a repository admin adds **SlopCop** as a required
status check in the branch's protection rule or ruleset. A neutral check
counts as passing for GitHub, so an incomplete check does not block a merge on
its own; read its details before merging.

## Pull requests from forks

A fork's pull request is checked when its author is an owner, member, or
collaborator of the repository. Anyone else's gets a neutral check saying it
was not checked, so the repository does not run checks for outside
contributors without asking.

## When a push is not checked

| Check says | What to do |
| --- | --- |
| From outside the repository | A fork by someone who is not an owner, member, or collaborator. |
| The change is too large | A check takes at most 4 MiB and 2,000 files, and GitHub must produce the diff. Split the pull request, or run `slopcop check` on part of it. |
| The change cannot be checked | The diff touches a path a check refuses, such as one outside the repository. |
| GitHub did not return the change | Push again or re-run the check. |
| Out of credits | A workspace owner [tops up](/account/credits/), then re-runs the check. |
| The repository is turned off | A workspace owner turns it on in **Repositories**. See [Repositories](/account/repositories/). |
| At its limit | Re-run the check later, or raise the workspace's limit. |
| Need turning on again | The person who turned it on no longer manages the workspace. A manager turns **Check pull requests** on again. |
| A policy file is invalid | A [repository rule](/checks/repository-rules/) the pull request changes does not compile. Run `slopcop check` locally to see why. |

Binary files are left out of the change: their content is not in GitHub's
diff, and a binary file holds no finding's line.

## What it costs

A pull request check costs what any check costs: it uses one of the
workspace's 1,000 free checks each calendar month, then credits. The
repository itself is free. See [Credits and what it costs](/account/credits/)
and the [pricing page](https://slopcop.com/pricing).

## Compared with CI

[Gate a change in CI](/checks/ci/) runs the CLI in your own workflow, with
your choice of `--fail-on`, rules, and output formats. Pull request checks need
no workflow, token, or clone, and always use the CLI's defaults: every rule,
`--fail-on finding`.
