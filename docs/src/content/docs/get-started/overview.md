---
title: What is SlopCop?
description: "SlopCop is hosted static analysis for the changes a coding agent makes. What a check is, what it reports, which languages it covers, and what it is not."
sidebar:
  order: 1
---

SlopCop checks a change to a repository and reports what the change introduced:
every new finding with its severity, the rule that raised it, the file and line,
and the certainty behind it. The analysis is
[Bifrost](https://github.com/BrokkAi/bifrost), an open-source static analysis
engine written in Rust, rather than a language model. A finding traces back to a
rule and to the lines that prove it, never to a guess.

It is built to run inside a coding agent's loop. The agent edits, runs
`slopcop`, reads what came back, fixes it, and checks again, all before you
push. The same command gates a change in CI.

## The pieces

| Piece | What it is |
| --- | --- |
| **The `slopcop` CLI** | An open-source Go binary ([BrokkAi/slopcop](https://github.com/BrokkAi/slopcop)). It builds one git patch from your base commit to your working tree, sends it to the service, and prints the report. It carries the agent skill and the Stop hook that teach Claude Code and Codex to use it. |
| **The hosted check service** | [slopcop.com](https://slopcop.com). It keeps its own clone of your repository, applies the patch at the base commit, runs the hosted policy catalog plus any repository rules you selected, and answers with structured results. |
| **The web app** | Where you sign in, approve a CLI login, see what your agents' checks found and ignored, manage workspace members and repositories, and add credits. |

There is no local analyzer and nothing to install per language. The CLI never
reports a check as passed without the service: every check it sends makes one
request, and a clean result always comes from the service. `--if-enabled` can
skip a repository the workspace has not enabled, and marks that as `skipped`,
never as clean.

## What a check reports

A check analyzes the files the change writes and reports the findings on the
lines it added or touched. Findings that were already in the code are counted
but never fail the check and are not listed, so a change that introduces
nothing passes however many findings the repository already holds.

Each finding carries:

- the **rule** that raised it and its **severity** (`error`, `warning`, `note`);
- its **certainty**: `definite` when Bifrost proved the pattern holds, `possible`
  when the match depends on something it could not settle, `heuristic` for a
  text-pattern match such as a secret-like string;
- the **file and line**, and for data-flow rules the path the value took;
- an **id** you can pass to `slopcop ignore` when it is a false positive.

A report also says what it could not cover. A rule that could not finish makes
the check `incomplete`, never `clean`. See [Read a report](/checks/read-a-report/).

## Languages

C, C++, C#, Go, Java, JavaScript, Kotlin, PHP, Python, Ruby, Rust, Scala, and
TypeScript. Coverage differs by language; the data-flow rules go deepest where
Bifrost has a full semantic model. The
[Bifrost documentation](https://bifrost.brokk.ai) describes each language's
front end.

## What it is not

- **Not a language model review.** A check never sends your code to a model.
  The analysis is deterministic: the same code gives the same result.
- **Not a build.** The service reads source as written. It never builds or runs
  your code, so half-finished edits can be checked.
- **Not a local tool.** The engine runs only in the service. The CLI has no
  offline mode and no embedded analyzer.
- **Not a replacement for tests.** It finds what static analysis can prove:
  injection paths, misuse of APIs, secret-like strings, structural rules your
  repository declares. It does not know whether your feature works.

## Where to go next

- [Quickstart](/get-started/quickstart/): install, sign in, and run a check.
- [SlopCop in your agent](/agents/overview/): how Claude Code and Codex use it.
- [What leaves your machine](/account/privacy/): exactly what a check sends.
