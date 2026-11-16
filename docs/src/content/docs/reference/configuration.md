---
title: Configuration
description: "Where slopcop keeps its credential and state, how the service URL, token, and workspace are resolved, and the files it writes in your home directory."
sidebar:
  order: 2
---

The CLI stores settings in the user config. `slopcop config get|set|unset`
manages `mode`, `review.reviewers`, `review.fallbacks`, `review.timeout`, and
`review.minCompleted`. Adapter commands are
edited in the config file.

## Precedence

| Value | Order |
| --- | --- |
| Service URL | `--service`, `SLOPCOP_SERVICE`, the default stored by `slopcop login --default` or `slopcop service use`, `https://slopcop.com` |
| Token | `SLOPCOP_TOKEN`, the login stored for that service |
| Workspace (`check`, `ignore`, `unignore`, `ignored`, `repo`) | `-w`/`--workspace`, `SLOPCOP_WORKSPACE`, the one the login is bound to |
| Base commit (`check`) | `--base-exact`, `--base`, merge base with the upstream, merge base with `origin/HEAD`, `HEAD` |
| Policy selection (`check`) | `--policy` files, plus `.slopcop/policies/**/*.rqlp` unless `--no-policy-discovery` |

## Files the CLI writes

All under the user config directory: `~/.config/slopcop/` on Linux
(`$XDG_CONFIG_HOME/slopcop/` when set), `~/Library/Application
Support/slopcop/` on macOS, `%AppData%\slopcop\` on Windows.

| File | Purpose |
| --- | --- |
| `config.json` | default service URL, one login per service, mode (unset: `both` where reviewers are configured and not in CI, otherwise `static`), and review settings, written owner-only; logout forgets one service's login |
| `update-notice` | the time of the last [upgrade notice](/get-started/install/#upgrade-notices), so it prints at most once a day |
| `skill-backups/<harness>-<time>/` | a copy of a skill with local edits that an update replaced; see [The agent skill](/agents/skill/) |

In each agent's skills directory, `slopcop/.slopcop-skill.json` records the
version that wrote the skill and what each file held.

## Files the CLI edits

`slopcop init` edits the user-level settings file for each installed harness:
Claude Code's `settings.json`, Codex's `hooks.json`, and Grok's
`hooks/slopcop.json`. Each gets SlopCop's static Stop hook and push gate without
disturbing other entries. Init also installs both skills in the user-level
locations described above. An existing per-repository install is removed when
init runs in that checkout, and migrates itself the first time its old hook
runs the upgraded CLI elsewhere.
`slopcop init --remove-hooks` removes SlopCop's user-level and legacy
repository hooks and nothing else. See [The Stop hook](/agents/stop-hook/).

## Files in the repository

| Path | Purpose |
| --- | --- |
| `.slopcop/policies/**/*.rqlp` | [repository rules](/checks/repository-rules/), evaluated by every check |

The repository's URL for the static half comes from the `origin` remote.
The review needs no supported origin.

## Review settings and state

The config's `mode` is `both`, `static`, or `ai`. `review.reviewers` is an
ordered list of 1–4 family names; duplicates are allowed. Set it with
`slopcop config set review.reviewers terra,sol`, or inspect and remove it with
`get` and `unset`. `review.timeout` defaults to `20m`.

A reviewer that cannot start does not stop the others, and one that ends
without a verdict is retried once in a fresh session (not after a timeout, a
missing login or exhausted credits). `review.fallbacks` is an ordered list of
up to 4 families that stand in for a reviewer that still fails, preferring one
on another provider. `review.minCompleted` (default 2, at most the number of
reviewers) is how many reviewers, primaries or fallbacks, must finish for a
clean review. It tolerates a failure only when more reviewers can run than it
requires: with `terra,sol`, no fallbacks and the default, one failure fails the
review. Mix providers (Codex for `terra` and `sol`, Claude for `haiku`, dsh for
`flash`) between reviewers and fallbacks, so one outage or empty account does
not fail every check. Adapter command overrides
are keyed by adapter at `review.adapters.<codex|dsh|claude>.command`. For
example, keep a DeepSeek key in a file with:

```json
"dsh": {"command": ["npx", "--yes", "@brokkai/dsh-acp@0.6.0", "--api-key-file", "/path/to/deepseek_api_key"]}
```

Otherwise dsh reads `DEEPSEEK_API_KEY`.

When `review.reviewers` is unset, `slopcop init` picks reviewers by sending
each family a short prompt. It tries different adapters at the same time and
prints each result as it arrives. It skips flash without a dsh command override
when `DEEPSEEK_API_KEY` is not set.

Under `<git-dir>/slopcop/`, the CLI keeps `last.json`, `findings.json`,
`overrides.jsonl`, and `review-*/` prompt and reviewer logs. Reviewer probe
logs are kept under `probes/` beside the user config because init is no longer
repository-specific. When a reviewer or probe is done, the CLI asks its adapter
to delete the session, so reviews
do not pile up in your agent's history: Codex archives the thread (`codex
unarchive <id>` restores it) and Claude deletes it. Cleanup is skipped when an
adapter does not support it, and a failed cleanup never affects the review. Checks that
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

Each service keeps its own login, and a login is only sent to the service that
issued it. `SLOPCOP_SERVICE` in the environment saves repeating the flag;
`slopcop service use <url>` makes a deployment the default. See
[Sign in](/account/sign-in/#the-service-url).
