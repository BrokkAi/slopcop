---
title: The web app
description: "What slopcop.com does besides approving a login. Accounts and sign-in, Agent setup, the Repositories and Checks pages, members and workspaces, and credits."
sidebar:
  order: 6
---

The CLI does the checking. The web app at [slopcop.com](https://slopcop.com)
is where you sign in, approve a CLI login, see what your agents' checks found,
and run the workspace.

## Accounts and sign-in

Sign up with an email address and a password, proved by a six-digit code sent
to the address, or with Google. Signing in takes the password, or a code
instead, which also covers a forgotten password. Once signed in, Account
settings let you change your display name and username, set a password, add a
passkey (then **Sign in with a passkey** asks for no email), and turn on
two-step verification with a passkey, an authenticator app, or recovery codes.

A new account lands on **Agent setup**, as every sign-in does.

## Agent setup

The install and sign-in steps from the [Quickstart](/get-started/quickstart/),
with the commands to copy. The device-flow approval page, where you enter or
confirm the code `slopcop login` printed, is at `/device`; the link the CLI
prints opens it with the code filled in.

## Repositories

Lists every repository of the active workspace. Repositories join when an agent
checks one with `slopcop check`; the page has no way to add one. Each row links
to the repository's page and its settings, says whether it is disabled, and for
the owner offers **Disable** and **Enable**. When the
[active-repository limit](/account/repositories/#the-active-repository-limit)
applies, the page says how many are active and lets the owner choose which stay
so.

When one of the workspace's GitHub, GitLab, or Bitbucket connections needs to
be reconnected, the page names it with a link to the workspace's settings.

A repository's settings hold what the CLI leaves to the web app: deletion,
notifications, managed suppressions, custom rules, historical analysis, and
automatic issue filing.

## Checks

**Checks** says what `slopcop check` found in the active workspace and how
often agents ignored it, for the last 24 hours, 7 days (the default), or 30
days. It shows the period's totals (checks answered, those that answered a
finding, the findings, and how many are ignored), then a row per rule, most
ignored first, with its checks, findings, ignored count and share, and up to
three recent examples. A finding counts once per repository, rule, and
identity, however many checks answered it.

It is the page to read when a rule seems noisy: a high ignored share on one
rule is a rule to fix or a false-positive pattern to report.

Examples from a private repository are shown only while your own connection can
read it.

## Workspaces and members

The Organization page manages the account's workspaces, their members, and
pending invitations. The owner of a team workspace renames it, invites
colleagues by email (invitations expire after 24 hours), makes a member a
manager and back, removes members, and can archive the workspace and restore
it. [Workspaces](/account/workspaces/) lists what each role may do.

## Credits

The Credits page shows the workspace's balance, what each kind of run costs,
the free-scan allowance, and where credits went. The owner buys credits there,
sets billing alerts, and can turn on auto top-up. A coupon you hold can be
applied to a workspace from the same page. See
[Credits and what it costs](/account/credits/).

## Scans

Besides checks, the web app can scan a whole repository: by hand, on a push or
pull request through the GitHub App, or over the repository's history. Scans
use credits and the daily free-scan allowance, and produce reports with
source-linked findings. They are a separate surface from the CLI's checks: a
check never starts a scan and never needs one to have run.
