---
title: Workspaces
description: "A login checks in, and bills, one workspace. How to see which, move a login, check in another workspace for one command, and what each role in a team workspace may do."
sidebar:
  order: 2
---

A login runs its checks and ignores in, and bills, one workspace: the one
active in the browser when the login was approved.

```console
slopcop whoami                     # account, this login's workspace, its plan, the service URL
slopcop workspace list             # the account's workspaces; * marks this login's
slopcop workspace use "Acme team"  # move this login (not the browser) to another, by name or id
slopcop check -w "Acme team"       # check in, and bill, another workspace this once
```

`whoami` and `workspace list` take `--format human|json`, as does
`workspace use` for its answer.

## Which workspace pays

When it matters which workspace a check lands in, run `slopcop whoami` before
it. It prints the account, the workspace (name, kind, role), its plan, and the
service URL; `--format json` gives `service`, `account`, `workspace`, and
`plan`.

`slopcop workspace use <name|id>` moves this login to another workspace, and
with it every agent and script using the login, not only the one that runs it.
So it asks `[y/N]` and refuses when stdin is not a terminal. An agent cannot
do it for you.

To check, ignore, or list repositories in another workspace without moving
anyone, `check`, `ignore`, `unignore`, `ignored`, and `repo` take `-w`/`--workspace <name|id>` (or
`SLOPCOP_WORKSPACE`). It applies to that command only. Give agents that share a
login their own `--workspace` instead of switching it.

Names match without regard to case; when two workspaces share a name, pass the
id the listing prints. A name that matches nothing answers
`workspace_not_found` with the listing to choose from.

## Personal and team workspaces

Every account owns one **personal workspace**, which never has another member.
A **team workspace** is governed by its members: the member holding the `owner`
role can rename it and invite colleagues by email from the Members panel, and
managers run its repositories day to day. An account can belong to twenty
workspaces in all, its personal one included.

Workspaces, members, and invitations are managed on the web app's Organization
page; see [The web app](/account/web-app/).

## Roles

A team workspace has one **owner**, and any number of **managers** and
**members**. A personal workspace has only its owner. What matters for the CLI:

| Capability | Member | Manager | Owner |
| --- | --- | --- | --- |
| Run checks, and add a repository with a first check (within the workspace's limits) | yes | yes | yes |
| Ignore a false positive | yes | yes | yes |
| Unignore a finding | yes | yes | yes |
| Read repository settings, rules, suppressions, and run history | yes | yes | yes |
| Report false positives and manage suppressions and custom rules in the web app | no | yes | yes |
| Choose which repositories are active: `repo enable`, `repo disable` | no | no | yes |
| Delete a repository and its records | no | no | yes |
| Invite and remove members; make a member a manager and back | no | no | yes |
| Buy credits, billing alerts, auto top-up | no | no | yes |
| Rename or archive the workspace | no | no | yes |

A command that needs the owner answers `owner_required` with a remedy naming
`--workspace`, in case the login is bound to the wrong workspace.
