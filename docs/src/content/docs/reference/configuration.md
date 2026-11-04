---
title: Configuration
description: "Where slopcop keeps its credential and state, how the service URL, token, and workspace are resolved, and the files it writes in your home directory."
sidebar:
  order: 2
---

The CLI stores settings in the user config. `slopcop config get|set|unset`
manages `mode`, `review.count`, and `review.timeout`. Adapter commands are
edited by hand.

## Precedence

| Value | Order |
| --- | --- |
| Service URL | `--service`, `SLOPCOP_SERVICE`, stored by `slopcop login`, `https://slopcop.com` |
| Token | `SLOPCOP_TOKEN`, stored value |
| Workspace (`check`, `ignore`, `unignore`, `ignored`, `repo`) | `-w`/`--workspace`, `SLOPCOP_WORKSPACE`, the one the login is bound to |
| Base commit (`check`) | `--base-exact`, `--base`, merge base with the upstream, merge base with `origin/HEAD`, `HEAD` |
| Policy selection (`check`) | `--policy` files, plus `.slopcop/policies/**/*.rqlp` unless `--no-policy-discovery` |

## Files the CLI writes

All under the user config directory: `~/.config/slopcop/` on Linux
(`$XDG_CONFIG_HOME/slopcop/` when set), `~/Library/Application
Support/slopcop/` on macOS, `%AppData%\slopcop\` on Windows.

| File | Purpose |
| --- | --- |
| `config.json` | service URL, token, mode (default `both`), and review settings, written owner-only; logout clears the token |
| `update-notice` | the time of the last [upgrade notice](/get-started/install/#upgrade-notices), so it prints at most once a day |
| `skill-backups/<harness>-<time>/` | a copy of a skill with local edits that an update replaced; see [The agent skill](/agents/skill/) |

In each agent's skills directory, `slopcop/.slopcop-skill.json` records the
version that wrote the skill and what each file held.

## Files the CLI edits

`slopcop init` runs in the repository. It edits `.claude/settings.local.json`
(static Stop hook and Claude push gate), `.codex/hooks.json` (static Stop
hook), and `.git/info/exclude`. It installs the skills in `.claude/skills/`
and `.agents/skills/`, leaving tracked targets alone. It removes old
user-level SlopCop hooks and skills. No Codex push gate is installed.
`slopcop init --remove-hooks` removes SlopCop's hook and nothing else. See
[The Stop hook](/agents/stop-hook/).

## Files in the repository

| Path | Purpose |
| --- | --- |
| `.slopcop/policies/**/*.rqlp` | [repository rules](/checks/repository-rules/), evaluated by every check |

The repository's URL for the static half comes from the `origin` remote.
The review needs no supported origin.

## Review settings and state

The config's `mode` is `both`, `static`, or `ai`. `review.count` is 1–3
(default 2) and `review.timeout` defaults to `20m`. Set these with
`slopcop config set <key> <value>`, or inspect and remove them with `get`
and `unset`. Adapter argv arrays under
`review.adapters.<terra|flash|haiku>.command` are edited by hand. The
default commands use pinned packages through `npx`: Codex ACP 2.5.0,
dsh ACP 0.6.0, and Claude agent ACP 0.100.0.

Under `<git-dir>/slopcop/`, the CLI keeps `last.json`, `findings.json`,
`overrides.jsonl`, and `review-*/` prompt and reviewer logs. Checks that
approve the tree (see [Exit codes](/checks/exit-codes/#which-results-approve-the-tree))
and valid overrides write `refs/slopcop/reviewed/<tree>`.

## Push approval level

Set `gate.blockOn` by editing the user config file. It defaults to `error`;
values are `never`, `finding`, `note`, `warning`, and `error`. New findings
at or above that level block push approval. Unknown severities count as
errors. This setting has no check flag and cannot be changed through
`slopcop config`.

## Another deployment

```console
slopcop --service https://staging.slopcop.com login
slopcop --service https://staging.slopcop.com
```

A login is stored with the service it was made against. `SLOPCOP_SERVICE` in
the environment saves repeating the flag.
