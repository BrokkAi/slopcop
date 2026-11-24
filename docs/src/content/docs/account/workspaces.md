---
title: Workspaces
description: "Which workspace a check runs in and bills. How to see it, map a repository to a workspace, move a login, check in another workspace for one command, and what each role in a team workspace may do."
sidebar:
  order: 2
---

A login is bound to one workspace: the one active in the browser when the
login was approved. Its checks and ignores run in, and bill, that workspace,
unless this machine maps the repository to another of your workspaces. Map
your work repositories to a team workspace and the same login checks them
there, and everything else in your personal one.

```console
slopcop whoami                     # account, this login's workspace, its plan, the service URL
slopcop status                     # this checkout's repository, and the workspace that bills it
slopcop enable                     # optional: add this checkout's repository to that workspace now
slopcop workspace list             # the account's workspaces; * marks this login's
slopcop workspace map "Acme team"  # check this checkout's repository there from now on, at a terminal
slopcop workspace unmap            # go back to the login's workspace for this repository
slopcop workspace mappings         # the repositories this machine maps, and to which workspace
slopcop workspace use "Acme team"  # move this login (not the browser) to another, by name or id
slopcop check -w "Acme team"       # check in, and bill, another workspace this once
```

`whoami`, `status`, `workspace list` and `workspace mappings` take
`--format human|json`, as does `workspace use` for its answer.

## Which workspace pays

A command runs in, and bills, the first of these that names a workspace:

1. `-w`/`--workspace <name|id>` on the command.
2. `SLOPCOP_WORKSPACE`.
3. The workspace this machine maps the checkout's repository to.
4. The workspace the login is bound to.

When it matters which workspace a check lands in, run `slopcop status` in the
checkout before it. It prints the repository and its state, the account, the
workspace (name, kind, role), and a `Chosen by` line saying which of the four
picked it; `--format json` has `workspace` and `workspaceSource` (`flag`,
`env`, `mapping` or `login`). `slopcop whoami` prints the login's own
workspace, and a `Checkout` line when the checkout it runs in is mapped
elsewhere.

None of the four chooses the workspace that runs a repository's
[pull request checks](#pull-request-checks-have-their-own-workspace).

## A workspace per repository

`slopcop workspace map <name|id>` makes every checkout of the repository the
`origin` remote names, on this machine, check in the workspace you name. The
Stop hook, `ignore`, `unignore`, `ignored`, `dictionary` and `repo` follow it
when they run in that checkout. `slopcop enable` maps the checkout to the
workspace it adds the repository to, so a repository checks where you enabled
it without another step; `slopcop enable --workspace <name>` adds it to another
and moves the mapping there.

The mapping decides which workspace pays, so `workspace map` and `workspace
unmap` ask `[y/N]` and refuse when stdin is not a terminal. An agent can
pass `--workspace` for one command instead. The one exception is `slopcop
enable`, which maps the checkout to the workspace it has just added the
repository to without asking, and says which one.

Mappings are kept in the CLI's `config.json`, by service and by the
repository's `https://host/owner/name` URL, so they cover every clone and
worktree of the repository on the machine, and stay when you sign out or sign
in again. They are not shared with other machines or with the web app.

If the workspace a repository is mapped to is archived, or your account leaves
it, checks of that checkout stop with `workspace_not_found` and say how to map
it again. They do not fall back to the login's workspace, which would then be
billed without your choosing it.

A check adds a repository its workspace has never added. When it does, it
says which workspace that was, how it was chosen, and how to change it, on
stderr and as `addedToWorkspace` in `json`, so an agent that ran the check can
tell you. To move the repository, run `slopcop workspace map <name>` in the
checkout; the owner of the workspace it was added to removes it there with
`slopcop repo disable`.

A checkout with no mapping checks in the login's workspace, as before. The
first check of such a repository, when your account has more than one
workspace, says so once, and names any other workspace of yours that already
has the repository, in case the check is landing in the wrong one. The check
still runs.

## Pull request checks have their own workspace

[Pull request checks](/checks/pull-requests/) do not follow any of this. A
mapping, `--workspace` and the login's workspace choose where the checks this
machine sends run. A pull request is checked by a workspace that has **Check
pull requests** on for the repository in the web app, and when more than one
has, by [the first to have turned it on](/checks/pull-requests/#more-than-one-workspace).
So a repository can check locally in your team workspace while its pull
requests are checked, and billed, in your personal one.

To see which workspace that is:

- A pull request's **SlopCop** check names the workspace that ran it, at the
  end of its summary.
- `slopcop status` prints an `Also in` line naming your account's other
  workspaces that have the repository, each with its state there, and
  `otherWorkspaces` in `json` (`id`, `name`, `kind`, `role`, `state`). Those
  and the workspace above it are the ones that can run its pull request
  checks. A workspace the service would not answer for is listed as `could not
  be asked`, `unknown` in `json`.

To move it, a manager or owner turns **Check pull requests** off in the
repository's **Settings** in the workspace that runs them now, and on in the
one that should; then `slopcop workspace map <name>` in the checkout moves the
local checks to match. The CLI cannot read or change the setting, so `status`
does not say which of the workspaces has it on, and `workspace map` says that
it leaves pull request checks where they are.

## Moving the login

`slopcop workspace use <name|id>` moves this login to another workspace, and
with it every agent and script using the login, not only the one that runs it.
So it asks `[y/N]` and refuses when stdin is not a terminal. An agent cannot
do it for you.

To check, ignore, or list repositories in another workspace once, without
moving anyone or mapping anything, `check`, `status`, `ignore`, `unignore`,
`ignored`, and `repo` take
`-w`/`--workspace <name|id>` (or `SLOPCOP_WORKSPACE`). It applies to that
command only. Give agents that share a login their own `--workspace` instead
of switching it.

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
| Run checks, which add the checkout's repository on its first check, as `slopcop enable` does (within the workspace's limits) | yes | yes | yes |
| Ignore a false positive | yes | yes | yes |
| Unignore a finding | yes | yes | yes |
| Read repository settings and check history | yes | yes | yes |
| Turn a repository's pull request checks on or off in the web app | no | yes | yes |
| Choose which repositories are active: `repo enable`, `repo disable` | no | no | yes |
| Delete a repository and its records | no | no | yes |
| Invite and remove members; make a member a manager and back | no | no | yes |
| Buy credits, billing alerts, auto top-up | no | no | yes |
| Rename or archive the workspace | no | no | yes |

A command that needs the owner answers `owner_required` with a remedy naming
`--workspace`, in case the login is bound to the wrong workspace.
