---
title: Repositories
description: "A first check adds a repository to the workspace. Listing, enabling, and disabling repositories, the active-repository limit, and the confirmation an agent cannot give."
sidebar:
  order: 3
---

```console
slopcop repo list                  # the workspace's repositories: active, disabled, or over the limit
slopcop repo disable acme/widgets  # stop checking and charging for one (owner only)
slopcop repo enable acme/widgets   # turn it back on, within the workspace's active limit
```

`repo list` takes `--format human|json`, as do `repo enable` and
`repo disable` for their answer. All three take `-w`/`--workspace` for another
workspace of the account.

## A first check adds a repository

There is no `repo add`: the first check of a repository adds it to the
workspace. The web app has no way to add one either. A public repository works
at once; a private one needs the workspace's
[GitHub App, GitLab, or Bitbucket connection](/account/private-repositories/).

A workspace's first repository is added without asking. When the workspace
would then pay for a repository (it already has an active repository and is
charged), the service refuses the check with `repository_add_unconfirmed` until
a person confirms:

- **At a terminal**, `check` prints what it costs, asks
  `Add owner/name to "Workspace"? [y/N]`, and on a yes sends the check again and
  says `added owner/name to "Workspace"` on stderr. A no ends with exit `4`.
- **With no terminal**, as for an agent or CI (a pipe or the null device is not
  one), nothing is asked: the check exits `4` with the service's remedy, a line
  naming the command that confirms it, and, in `json`, a `failed` report naming
  the code.

Once the person has approved it, `check --add-repository --workspace <name|id>`
confirms without a terminal. The flag needs `--workspace` (the flag itself, not
`SLOPCOP_WORKSPACE`), so an approval given for one workspace cannot add the
repository to the one the login happens to point at. The flag changes nothing
for a repository already in that workspace.

An agent following the skill stops at `repository_add_unconfirmed` and tells
you the repository, the workspace, and the cost. It never passes
`--add-repository` without your approval, and never picks a workspace you did
not name.

## Active and disabled

Every repository a workspace has is **active** or **disabled**. Every active
repository after a workspace's first costs credits every 30 days
([Credits](/account/credits/)); a disabled one is not checked or charged, and
keeps its reports, findings, and settings. A check of a disabled repository
answers `repository_disabled`; `slopcop whoami` names the workspace that
disabled it.

Only the workspace's owner enables or disables, and `repo enable` and
`repo disable` ask you to type the repository's `owner/name` and refuse when
stdin is not a terminal. An agent or CI job runs with your login, and the
service cannot tell it from you, so changing what the workspace pays stays a
step you take yourself.

Names match without regard to case; when two providers have a repository of
the same name, pass the id the listing prints.

## The active-repository limit

A workspace that has never paid (never topped up, had a coupon applied, or been
unlocked by an operator) has no limit for its first 45 days, then **one**
active repository until it unlocks. A workspace that has paid once has no limit,
for good. `repo list` marks a repository that is enabled but over the limit, and
the web app's Repositories page is where the owner chooses which stay active.
Ten days before the limit applies, the workspace is told by email and in the
app. A check of a repository over the limit answers
`active_repository_limit_reached`.

## What stays in the web app

Deleting a repository and its records, archiving a workspace, members, credits,
and repository settings (automatic issue filing, notifications, suppressions,
custom rules) stay in the web app. See [The web app](/account/web-app/).
