---
title: Sign in
description: "The browser device flow, tokens for CI and agents, where the credential is stored, the service URL, and the order in which values are resolved."
sidebar:
  order: 1
---

```console
slopcop login                           # browser device flow
slopcop login --token "$SLOPCOP_TOKEN"  # existing credential, for CI and agents
slopcop logout                          # forget the stored credential
```

## The browser flow

`slopcop login` prints a link that opens the approval page with the code filled
in, plus the page and code to type in instead, and waits. Approve it in a
browser where you are signed in to [slopcop.com](https://slopcop.com), and only
if the page shows the code your terminal printed. Once approved, the CLI stores
the token and says so.

The login checks in, and bills, the workspace that was active in your browser
when you approved it. `slopcop whoami` names it; see
[Workspaces](/account/workspaces/).

Login also installs the [agent skill](/agents/skill/) into every skills
directory whose harness is installed here.

## Tokens

`--token` stores an existing credential instead of starting the browser flow,
which is what CI and agent harnesses use. `SLOPCOP_TOKEN` in the environment
supplies a token for one run with no stored login at all, and wins over a
stored one.

A token is tied to the account and workspace it was approved in. Treat it as a
password: a check sends your working tree with it.

## Where the credential lives

The credential lives in `slopcop/config.json` under the user config directory
(`~/.config/slopcop/config.json` on Linux, `~/Library/Application
Support/slopcop/config.json` on macOS, `%AppData%\slopcop\config.json` on
Windows) and is written owner-only. `slopcop logout` clears it. Next to it the
CLI keeps the time of the last [upgrade notice](/get-started/install/#upgrade-notices)
and any [skill backups](/agents/skill/#local-edits-are-kept).

## The service URL

The service defaults to `https://slopcop.com`. `--service`, `SLOPCOP_SERVICE`,
or a value stored by `slopcop login` override it, for another deployment such as
staging:

```console
slopcop --service https://staging.slopcop.com login
slopcop --service https://staging.slopcop.com
```

A login against one service is stored with that service's URL. A deployment
without a browser login answers `device_flow_disabled`; use `--token` there.

## Precedence

| Value | Order |
| --- | --- |
| Service URL | `--service`, `SLOPCOP_SERVICE`, stored by `slopcop login`, `https://slopcop.com` |
| Token | `SLOPCOP_TOKEN`, stored value |
| Workspace (`check`, `ignore`, `repo`) | `-w`/`--workspace`, `SLOPCOP_WORKSPACE`, the one the login is bound to |

## Accounts

An account is free. Sign up at [slopcop.com](https://slopcop.com) with an email
address and a password (a six-digit code proves the address), or with Google.
Once signed in you can add a passkey and turn on two-step verification in
Account settings. Every account owns one personal workspace; see
[The web app](/account/web-app/).
