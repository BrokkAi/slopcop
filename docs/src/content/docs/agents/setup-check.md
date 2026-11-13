---
title: Checking a checkout's setup
description: "How to tell whether a checkout or worktree has SlopCop's skill and hooks: slopcop doctor, the opt-in session-start reminder for Claude Code and Codex, what each harness covers, and the setup steps when nothing can remind you."
sidebar:
  order: 7
---

`slopcop init` writes the skill and hooks into one checkout, and leaves them
out of Git. A fresh clone, or a new worktree of a repository you set up, starts
without them. Nothing inside that checkout can say so: its hooks are the very
thing that is missing. Two tools cover this.

## `slopcop doctor`

```console
slopcop doctor                 # in the checkout
slopcop doctor --format json   # for an agent or a script
```

Doctor reports whether the agents installed here would load the `slopcop`
skill and run the Stop hook and push gate in this checkout, and the `slopcop
init` command that adds what is missing. It reads local files and Git
configuration only. It sends nothing, probes no reviewer, and changes
nothing, so an agent can run it at any time.

| State | Meaning |
| --- | --- |
| `ready` | nothing is missing |
| `not_set_up` | init has never run in this repository and nothing is installed |
| `incomplete` | something is missing: a fresh worktree of a set-up repository, a hook removed by hand, or a harness installed since init ran |
| `not_git` | not a Git repository; init needs one |

A skill or hook counts wherever its harness reads it: in the checkout, or at
user level. Doctor cannot see Codex trust. Codex runs a repository's hooks
only in a project you trust and after you trust the hooks with `/hooks`, so
doctor reminds you of that whenever Codex hooks are involved.

## What you turned down stays turned down

Init records its choices in the repository's Git configuration, which every
worktree of the repository shares:

| Key | Set by |
| --- | --- |
| `slopcop.init` | every init, to the version that ran |
| `slopcop.skill` | `false` after `--no-skill`, otherwise `true` |
| `slopcop.hooks` | `false` after `--no-hooks` or `--remove-hooks`, otherwise `true` |

Doctor does not report a declined piece as missing. The command it suggests
carries the same `--no-skill` or `--no-hooks`, so following the advice never
brings back what you removed. Neither doctor nor the reminder below installs
anything.

## The session-start reminder

```console
slopcop init --session-check
```

This also installs a user-level `SessionStart` hook that runs `slopcop doctor
--hook claude` in `~/.claude/settings.json` (or under `CLAUDE_CONFIG_DIR`) and
`slopcop doctor --hook codex` in `~/.codex/hooks.json` (or under
`CODEX_HOME`). Only a user-level hook can run in a checkout that has none of
its own, which is why this is a separate opt-in.

When a session opens, the hook prints nothing unless all of these hold:

- the directory is in a Git repository;
- something is missing for that harness, after declined pieces are left out;
- the repository has not silenced the reminder;
- this checkout has not already been told about the same missing pieces.

Otherwise it gives the agent one note naming what is missing and the init
command to run, and asks it to check with you first, since init edits the
checkout's agent settings. Hooks added then take effect in the next session.
The note comes once per checkout. It comes again only if what is missing
changes. The hook runs as `... || true`, so a missing or older `slopcop`
never gets in the way of a session.

Silence it for one repository, or everywhere:

```console
git config slopcop.sessionCheck false
git config --global slopcop.sessionCheck false
```

`slopcop init --remove-hooks` removes the session check with the other hooks.

Codex runs the user-level hook only after you trust it with `/hooks`.

## Coverage

| Harness | Doctor | Session-start reminder |
| --- | --- | --- |
| Claude Code | yes | yes, with `--session-check` |
| Codex 0.166 and later | yes; trust not visible | yes, with `--session-check`, once trusted |
| Grok | no | no: its session-start output is not verified, and the reminder stays quiet when Grok runs Claude Code's hooks |
| Kimi Code, opencode, Cursor, Copilot CLI, others | skill only | no |

## Without a reminder

Where no reminder can run, make setup part of starting work in a new checkout
or worktree:

```console
slopcop doctor
slopcop init            # or the exact command doctor printed
```

A worktree created by a tool, such as an agent's own worktree feature, needs
this too: its untracked `.claude` and `.codex` files do not come along.
