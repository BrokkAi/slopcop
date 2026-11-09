---
title: Pi
description: "Pi reads the SlopCop skill from the repository's .agents/skills, has no Stop hook, and a check run by Pi sends the models its session file records."
sidebar:
  order: 4
---

## The skill

`slopcop init` puts the SlopCop skill in the repository's `.agents/skills/`,
which Pi reads, along with `.pi/skills`, in the working directory and each
parent up to the Git root. Pi loads a project's skills only once you trust the
project. Ask it to check its change, or force the skill with
`/skill:slopcop`.

To have Pi check every change, add the repository to the workspace from its
checkout with `slopcop enable` at a terminal, then add a line to your
project's `AGENTS.md`:

```text
Run `slopcop check --if-enabled` before you finish and fix what it reports.
```

## No Stop hook

Pi has no hook that runs a shell command: its hooks are TypeScript
extensions. `slopcop init` installs no hook for Pi, so the skill and the line
in `AGENTS.md` are how it knows to check.

## What the CLI reads from the session

Pi names its session in the commands its `bash` tool runs (`PI_SESSION_ID` and
`PI_SESSION_FILE`). From that session file, or from the one named for the
session under `~/.pi/agent/sessions` (`$PI_CODING_AGENT_DIR/sessions`, or
`$PI_CODING_AGENT_SESSION_DIR`), the CLI reads the files each successful
`edit` or `write` call wrote, including those a `codemode` script made, and
the model and thinking level of the message that called it. It reads only
those fields, never the conversation or a file's contents, and sends the
harness, the session id as an opaque identifier, and each model and thinking
level with the repository-relative files it wrote. Pi records no version of
itself, so none is sent. This is analytics only and never decides a check.
`slopcop --verbose` prints it.

## Not yet verified against a live session

This support follows Pi's session format as its source and documentation
define it. It has not yet been checked against a session Pi recorded, so a
check may send less than described until it is.
