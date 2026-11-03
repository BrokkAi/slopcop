---
title: Configuration
description: "Where slopcop keeps its credential and state, how the service URL, token, and workspace are resolved, and the files it writes in your home directory."
sidebar:
  order: 2
---

The CLI has no configuration file to edit. Everything is a flag, an environment
variable, or a value `slopcop login` stored.

## Precedence

| Value | Order |
| --- | --- |
| Service URL | `--service`, `SLOPCOP_SERVICE`, stored by `slopcop login`, `https://slopcop.com` |
| Token | `SLOPCOP_TOKEN`, stored value |
| Workspace (`check`, `ignore`, `repo`) | `-w`/`--workspace`, `SLOPCOP_WORKSPACE`, the one the login is bound to |
| Base commit (`check`) | `--base-exact`, `--base`, merge base with the upstream, merge base with `origin/HEAD`, `HEAD` |
| Policy selection (`check`) | `--policy` files, plus `.slopcop/policies/**/*.rqlp` unless `--no-policy-discovery` |

## Files the CLI writes

All under the user config directory: `~/.config/slopcop/` on Linux
(`$XDG_CONFIG_HOME/slopcop/` when set), `~/Library/Application
Support/slopcop/` on macOS, `%AppData%\slopcop\` on Windows.

| File | Purpose |
| --- | --- |
| `config.json` | the stored service URL and token, written owner-only; `slopcop logout` clears it |
| `update-notice` | the time of the last [upgrade notice](/get-started/install/#upgrade-notices), so it prints at most once a day |
| `skill-backups/<harness>-<time>/` | a copy of a skill with local edits that an update replaced; see [The agent skill](/agents/skill/) |

In each agent's skills directory, `slopcop/.slopcop-skill.json` records the
version that wrote the skill and what each file held.

## Files the CLI edits

`slopcop init` adds a Stop hook to Claude Code's `settings.json` (under
`CLAUDE_CONFIG_DIR` or `~/.claude`) and Codex's `hooks.json` (under
`CODEX_HOME` or `~/.codex`), leaving everything else in those files as it was.
`slopcop init --remove-hooks` removes SlopCop's hook and nothing else. See
[The Stop hook](/agents/stop-hook/).

## Files in the repository

| Path | Purpose |
| --- | --- |
| `.slopcop/policies/**/*.rqlp` | [repository rules](/checks/repository-rules/), evaluated by every check |

Nothing else in the repository is read for configuration. The repository's URL
comes from the `origin` remote.

## Another deployment

```console
slopcop --service https://staging.slopcop.com login
slopcop --service https://staging.slopcop.com
```

A login is stored with the service it was made against. `SLOPCOP_SERVICE` in
the environment saves repeating the flag.
