---
title: Managed rollout
description: "For administrators: deploy SlopCop's Stop hook through Claude Code managed settings or Codex's managed requirements instead of having each user run slopcop init."
sidebar:
  order: 8
---

`slopcop init` writes [the Stop hook](/agents/stop-hook/) into user-level
settings. An administrator who manages Claude Code or Codex across a fleet can
deploy the same hook through managed configuration instead.
Where managed configuration allows only managed hooks, this is the only way the
hook runs: the one init writes is a local hook, and it is blocked. So is
Claude Code's push gate, the `PreToolUse` hook on `Bash` that runs
`slopcop gate --hook claude`; deploy it the same way to keep it.

<!-- TODO: neither path below has been tested end to end yet (#35). Test each
one on a managed machine before relying on it. -->

## What every machine still needs

Managed configuration delivers the hook, not slopcop:

- **`slopcop` on `PATH`** for the user the agent runs as. The hook command
  is `slopcop hook claude` or `slopcop hook codex`, and neither harness
  installs it. Use the same tooling that places the managed file; see
  [Install the CLI](/get-started/install/).
- **A login.** The hook checks with the user's own credential: `slopcop login`,
  or `SLOPCOP_TOKEN` in the agent's environment. See [Sign in](/account/sign-in/).
- **An enabled repository.** The hook sends nothing for a repository the
  workspace does not have active, and never adds one. It stays dormant in a
  repository until someone runs `slopcop enable` there. See
  [Repositories](/account/repositories/).

To check that the `slopcop` a user's agent will find can run the hook, run it
as that user with no input. It answers with a JSON `systemMessage` and exits
`0`:

```console
$ slopcop hook claude </dev/null
{"systemMessage":"slopcop: could not read the Stop hook's payload, so no check ran."}
```

An older `slopcop` that cannot run the hook answers something else. The hook
command ends in `|| true`, so the agent still stops, but no check runs.

Users still get the skill from `slopcop init --no-hooks`, which installs the
skill and leaves hooks alone. It installs no push gate either. See [The agent skill](/agents/skill/).

## Claude Code

Claude Code reads hooks from [managed
settings](https://code.claude.com/docs/en/managed-settings), above every user
and project file. Add SlopCop's `Stop` handler to the `hooks` key of the
managed settings you deploy, exactly as `slopcop init` writes it:

```json
{
  "hooks": {
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "slopcop hook claude || true",
            "timeout": 720,
            "statusMessage": "Checking the change with SlopCop"
          }
        ]
      }
    ]
  }
}
```

As a file, that is `managed-settings.json` in Claude Code's system directory,
or a file of its own such as `50-slopcop.json` in the `managed-settings.d/`
directory next to it, which Claude Code merges with the rest:

| OS | Directory |
| --- | --- |
| macOS | `/Library/Application Support/ClaudeCode/` |
| Linux and WSL | `/etc/claude-code/` |
| Windows | `C:\Program Files\ClaudeCode\` |

MDM profiles and server-managed settings take the same `hooks` key. Claude
Code uses one managed source by default, so put the hook in the one in force on
the machine. On one machine, `/status` in Claude Code names it on the `Setting
sources` line.

Keep the handler identical to the one above:

- **The command.** `|| true` turns any failure of slopcop into one that lets
  the agent stop. Claude Code reads exit `2` from a `Stop` hook as an order to
  keep going, and a slopcop that cannot run the hook may exit `2`.
- **The timeout.** 720 seconds, so Claude Code waits longer than the hook's
  own 10-minute wait for a check's result.
- **The whole handler.** Claude Code runs a handler that is defined the same
  way in several settings files once. A user who also runs `slopcop init`
  then gets one check, not two.

### The push gate

To keep the [push gate](/agents/claude-code/) under managed hooks, add its
`PreToolUse` group beside the `Stop` one, exactly as `slopcop init` writes it:

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "^(Bash|mcp__.+__(create_pull_request|push_files))$",
        "hooks": [
          {
            "type": "command",
            "command": "slopcop gate --hook claude"
          }
        ]
      }
    ]
  }
}
```

Leave off `|| true` here: the gate blocks a push by exiting `2`. It allows
everything outside a Git repository and every command that does not push, so
it is safe to deploy machine-wide.

Under `allowManagedHooksOnly`, Claude Code blocks user, project, and local
hooks, including the Stop hook and push gate `slopcop init` writes to the
user's `settings.json`;
only the managed hooks run. Without it, user hooks run beside managed ones,
and a user's `slopcop init --remove-hooks` removes only their own copy, never
the managed one.

## Codex

Codex reads [managed hooks](https://developers.openai.com/codex/hooks) inline
from the `[hooks]` table of its managed `requirements.toml`. Hooks there are
trusted by policy, so users skip the `/hooks` trust step that the hook
`slopcop init` writes needs, and they cannot turn the hook off from Codex's
hook browser.

<!-- TODO: the TOML below follows Codex's hooks documentation and has not been
run against a managed Codex. Confirm the table names, whether
`[features] hooks = true` is required, whether a managed command must live
under `managed_dir`, and where requirements.toml is read from on each OS. -->

```toml
[hooks]

[[hooks.Stop]]

[[hooks.Stop.hooks]]
type = "command"
command = "slopcop hook codex || true"
command_windows = "slopcop hook codex || exit /b 0"
timeout = 720
```

The Windows command differs because Codex runs hooks there through `cmd`,
which has no `true`.

To keep the push gate, add its `PreToolUse` hook too. Codex sends every shell
command to it as `Bash`:

```toml
[[hooks.PreToolUse]]
matcher = "Bash"

[[hooks.PreToolUse.hooks]]
type = "command"
command = "slopcop gate --hook codex"
```

Codex does not install anything from `managed_dir`; device management must
put `slopcop` on `PATH` itself, as above.

`allow_managed_hooks_only = true` makes Codex ignore user, project, session,
and plugin hooks, including the one in the user's `hooks.json` that
`slopcop init` writes. Without it, give users `slopcop init --no-hooks`, so
they are not also asked to trust a second copy of the hook.
