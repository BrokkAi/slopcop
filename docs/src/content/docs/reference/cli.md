---
title: CLI reference
description: "Every slopcop command and flag. Check, enable, status, login, logout, whoami, workspace, repo, ignore, unignore, ignored, init, hook, and version."
sidebar:
  order: 1
---

```text
slopcop [flags]            # check the working tree (same as slopcop check)
slopcop [command] [flags]
```

Running `slopcop` with no subcommand runs the check. `slopcop --help` and
`slopcop <command> --help` print the same text as this page. `slopcop
completion <shell>` generates shell completion.

## Global flags

| Flag | Meaning |
| --- | --- |
| `--service URL` | the SlopCop check service URL (also `SLOPCOP_SERVICE`); default `https://slopcop.com` |
| `-v`, `--version` | print the version and exit |
| `-h`, `--help` | help for the command |

## `slopcop check`

Check the working tree through the hosted check API. See
[Run a check](/checks/run-a-check/).

```console
slopcop
slopcop check --if-enabled
slopcop check --format json
slopcop --root ../repo --base origin/main
```

| Flag | Meaning |
| --- | --- |
| `--root PATH` | repository path to check (default `.`) |
| `--base REV` | branch or revision the change started from; the base is its merge base with `HEAD` (default: merge base with the upstream branch, then with `origin/HEAD`, then `HEAD`) |
| `--base-exact REV` | exact base commit or revision, used without taking a merge base |
| `--format human\|json\|sarif` | report format (default `human`) |
| `--fail-on never\|finding\|note\|warning\|error` | exit `1` when a new finding is at or above this level (default `finding`) |
| `--policy PATH` | repository-relative `.rqlp` policy file to evaluate (repeatable) |
| `--no-policy-discovery` | do not include `.slopcop/policies` rules automatically |
| `--timeout DURATION` | how long to wait for the result; a check still running then exits `3` (default `10m0s`) |
| `-w`, `--workspace NAME\|ID` | workspace to run in, and bill, by name or id, instead of the one this login is bound to (also `SLOPCOP_WORKSPACE`) |
| `--if-enabled` | check only if the repository is active in the workspace; otherwise exit `0` without sending a check |
| `--add-repository` | add the repository to the workspace `--workspace` names, which then pays for it, without asking at a terminal; pass it once the user has approved that. A check without it never adds a repository |
| `--verbose` | print the resolved request to stderr, and name each inconclusive check in `human` |

Exit codes: `0` clean, `1` new findings, `2` incomplete or failed, `3` still
running, `4` refused and a person must act, `130`/`143` interrupted. See
[Exit codes](/checks/exit-codes/).

## `slopcop enable`

Add the repository the `origin` remote names to the workspace that checks and
bills it, and run its first check. A repository already added but disabled is
enabled again. The command names the workspace and the cost, asks you to type
the repository's `owner/name`, and refuses when stdin is not a terminal. It
exits with the first check's result. A check never adds a repository by
itself. See [Repositories](/account/repositories/).

| Flag | Meaning |
| --- | --- |
| `--root PATH` | repository path (default `.`) |
| `-w`, `--workspace NAME\|ID` | workspace to add it to, by name or id, instead of the one this login is bound to (also `SLOPCOP_WORKSPACE`) |

## `slopcop status`

Show the account and workspace that bill checks, and whether the repository the
`origin` remote names is added to that workspace and active. It is the quick
way to answer whether a checkout is handled by SlopCop. See
[Repositories](/account/repositories/).

| Flag | Meaning |
| --- | --- |
| `--root PATH` | repository path (default `.`) |
| `--format human\|json` | output format (default `human`) |
| `-w`, `--workspace NAME\|ID` | workspace to inspect, by name or id, instead of the one this login is bound to (also `SLOPCOP_WORKSPACE`) |

States: `active`; `disabled`; `enabled, over the limit`; `not added` (run
`slopcop enable` from the checkout to add it); and `unsupported` (SlopCop checks
repositories on github.com, gitlab.com and bitbucket.org). `json` carries the
same state as `active`,
`disabled`, `over_limit`, `not_added`, or `unsupported`, with the repository's
`url`, `fullName`, `provider`, `id`, `enabled`, and `active`. Exits `2` when
the checkout has no `origin`, or the service cannot answer.

## `slopcop login`

Sign in to the SlopCop check service. With no flags, starts the browser device
flow: prints a link that opens the approval page with the code filled in, then
waits for approval. Also installs the agent skill into every well-known skills
directory whose harness is installed here, as every check does. See
[Sign in](/account/sign-in/).

| Flag | Meaning |
| --- | --- |
| `--token TOKEN` | store an existing token instead of starting the browser flow |

## `slopcop logout`

Forget the stored credential.

## `slopcop whoami`

Show the account behind this login, the workspace it is bound to (the one its
checks and ignores run in, and bill), that workspace's plan, and the service
URL. Run it before a check when it matters which workspace pays.

| Flag | Meaning |
| --- | --- |
| `--format human\|json` | output format (default `human`); `json` has `service`, `account`, `workspace`, `plan` |

## `slopcop workspace`

List your account's workspaces, or bind this login to another of them. See
[Workspaces](/account/workspaces/).

| Command | Meaning |
| --- | --- |
| `workspace list [--format human\|json]` | the account's workspaces; `*` marks this login's |
| `workspace use <name\|id> [--format human\|json]` | bind this login to another workspace; moves every agent and script using the login, so it asks `[y/N]` and refuses when stdin is not a terminal |

## `slopcop repo`

List the repositories of this login's workspace, or of the one `--workspace`
names, or enable or disable one. See [Repositories](/account/repositories/).

| Command | Meaning |
| --- | --- |
| `repo list [--format human\|json]` | the workspace's repositories and whether each is active, disabled, or enabled over the limit |
| `repo enable <owner/name\|id>` | enable a repository, within the workspace's active limit (owner only; asks you to type the name) |
| `repo disable <owner/name\|id>` | disable a repository: it is not checked or charged (owner only; asks you to type the name) |

All take `-w`/`--workspace`. `enable` and `disable` take `--format` for their
answer, and refuse when stdin is not a terminal.

## `slopcop ignore <finding-id>`

Stop reporting one finding that is a false positive. The id is the one a check
printed. Ignoring covers that one finding in its repository for your workspace,
not its rule. Takes `-w`/`--workspace`. Exits `0` with a confirmation, `2` with
the service's message otherwise. See [False positives](/checks/false-positives/).

## `slopcop unignore <finding-id>`

Undo an ignore: later checks of the repository report the finding again. The id
is the one it was ignored from, or the same finding's id from any later check.
Unignoring a finding that is not ignored is fine. Takes `-w`/`--workspace`, and
exits like `ignore`. See [False positives](/checks/false-positives/#undo-an-ignore).

## `slopcop ignored`

List the findings your workspace ignored in this repository (the one `origin`
names), newest first, 25 per page: each one's finding id for `unignore`,
severity, location, rule, title, and when and by whom. A finding the service no
longer records has no id. Flags: `--page <n>` (from 1), `--format human|json`,
`--root <path>`, `-w`/`--workspace`. Exits `0`, or `2` with the service's
message.

## `slopcop init`

Install the agent skill that ships inside this binary, and a Claude Code,
Codex and Grok Stop hook that checks what a session wrote when the agent stops. See
[The agent skill](/agents/skill/) and [The Stop hook](/agents/stop-hook/).

| Flag | Meaning |
| --- | --- |
| `--no-skill` | do not install or update the agent skill |
| `--no-hooks` | do not install or update the automatic check hook |
| `--remove-hooks` | remove SlopCop's automatic check hook and nothing else |

## `slopcop hook <claude|codex|grok|kimi>`

Run a check from a Claude Code, Codex, Grok or Kimi Code Stop hook. It reads
the hook's input on stdin and answers in the harness's hook protocol.
`slopcop init` installs it as `slopcop hook claude || true`,
`slopcop hook codex || true` and `slopcop hook grok || true`; Kimi Code's is
added by hand (see [Grok and Kimi Code](/agents/grok-kimi/)). It is not meant
to be run by hand.

## `slopcop version`

Print the version. A release binary prints its tag; a plain `go install` prints
`dev`.

## Environment variables

| Variable | Meaning |
| --- | --- |
| `SLOPCOP_TOKEN` | a token for this run; wins over the stored credential |
| `SLOPCOP_SERVICE` | the service URL; `--service` wins over it |
| `SLOPCOP_WORKSPACE` | the workspace for `check`, `ignore`, `unignore`, `ignored`, and `repo`; `-w` wins over it |
| `SLOPCOP_NO_UPDATE_NOTICE` | any non-empty value silences the upgrade notice |
| `CLAUDE_CODE_SESSION_ID`, `CLAUDE_CONFIG_DIR` | set by Claude Code; name the session the CLI reads model attribution from |
| `CODEX_THREAD_ID`, `CODEX_HOME` | set by Codex; the same |
| `OPENCODE_SESSION_ID`, `OPENCODE_DB`, `XDG_DATA_HOME` | set by OpenCode or you; the same |
| `GROK_SESSION_ID`, `GROK_HOME` | set by Grok; the same |
| `KIMI_CODE_HOME` | Kimi Code's data directory, read by its Stop hook |

See [Configuration](/reference/configuration/) for precedence and the files
the CLI writes.
