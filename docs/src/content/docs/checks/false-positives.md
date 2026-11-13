---
title: False positives
description: "Ignore one finding with slopcop ignore so later checks of the repository leave it out. What an ignore covers, who it applies to, and what it does not do."
sidebar:
  order: 5
---

Each finding a check reports carries an `id`. When a finding is a false
positive, ignore it:

```console
slopcop ignore 0f8fad5b-d9cb-469f-a165-70867728950e
```

That finding is not reported again for this repository. Ignoring covers that
one finding, its rule plus its place in the code, in its repository for your
workspace; the rule keeps reporting everything else. Ignoring a finding twice
is fine.

## Ignore in the code

A finding can also be ignored with a comment next to the code, in the
language's own comment syntax:

```text
// slopcop: ignore[bifrost.security.sql-injection] -- the input is a constant
x = query(input)  // slopcop: ignore[bifrost.security.sql-injection]
// slopcop: ignore -- generated code, reviewed upstream
```

- A comment alone on its line covers the next line with code; one after code
  covers its own line.
- `[...]` lists the rules it ignores, by the finding's `policyId`, separated by
  commas. Without a list it ignores every finding on the line, and then it
  must give a reason after `--`, or it ignores nothing.
- Text after `--` is the reason; give one even when it is optional.

Use a comment when the code is right and a reviewer should see why: it shows
in the diff, moves with the line, and applies to everyone who checks the
repository. Use `slopcop ignore <id>` when the finding itself is wrong: it
holds for your workspace and leaves the code alone.

A comment never hides what it ignored. Every finding one ignored is still
listed, apart from the findings and without failing the check: in `human`
under *Ignored inline by slopcop: ignore*, in `json` under `inlineSuppressed`
with the comment's line and reason, and in `sarif` as a suppressed result.
A comment that ignores nothing is a warning, also never failing the check:
`unused` when it matches no finding (the rule or the line is wrong),
`missing_reason` for a list-less comment without a reason, and `malformed`
when it does not parse. Comments are read only in files where a check has a
finding, so a stale comment in a file without one is not reported.

## Read before you ignore

A `definite` finding is one Bifrost proved; it is rarely wrong. A `possible`
finding depends on something Bifrost could not settle, and a `heuristic` one is
a text match. Read the code before deciding either is a false positive, and
when it is, say so: the [Checks page](/account/web-app/#checks) of the web app
shows your workspace which rules its agents ignore most, which is how a noisy
rule gets fixed.

An agent following the skill does exactly this: it reads a `possible` or
`heuristic` finding, ignores it only when the code shows it is wrong, and tells
you.

## What an ignore is

- It applies in your workspace, for that repository. The same finding in
  another workspace is that workspace's.
- It uses the same login and service URL as a check, and `-w`/`--workspace` to
  ignore in another workspace this once.
- It exits `0` with a one-line confirmation, and otherwise `2` with the
  service's message and remedy. `finding_not_found` means the id is unknown or
  comes from a check outside your workspace: use one a check printed.
- The [Stop hook](/agents/stop-hook/) does not hand an ignored finding back to
  the agent.

An ignore is tied to the finding's identity, which follows the code: an
unrelated edit elsewhere in the file keeps it, while changing the flagged code
itself gives a new finding that is reported again.

When the service recognises such a finding as one you ignored, by its rule,
file and place, the report says so instead of presenting it as new: a
`Matches ignored finding <old id> (code changed)` line in `human`,
`previouslyIgnored` in `json`, and `properties["slopcop.previouslyIgnored"]` in
`sarif`, with the count in `remappedIgnores`. The old ignore does not cover it;
if it is still a false positive, `slopcop ignore` its new id.

## Undo an ignore

Ignored the wrong finding? Unignore it:

```console
slopcop unignore 0f8fad5b-d9cb-469f-a165-70867728950e
```

Later checks of the repository report it again. The id can be the one it was
ignored from, or the same finding's id from any later check of the repository.
Unignoring a finding that is not ignored is fine. It takes `-w`/`--workspace`
and exits like `ignore`.

Lost the id? `slopcop ignored` lists the findings your workspace ignored in
this repository, newest first, with each one's id, location, rule, and when and
by whom it was ignored. It shows 25 at a time; pass `--page 2` for the next.

## Exclude paths from checks

Some files are wrong on purpose, such as test inputs whose findings are their
expected output. Rather than ignore each finding, keep their paths out of
checks with a `[check]` section in `.slopcop/config.toml`, committed at the
root of the repository:

```toml
[check]
# Paths no rule checks (gitignore syntax).
exclude = ["vendor/**"]

# Paths only the listed rules skip: exact rule ids, or a prefix ending in `*`.
[[check.exclude_rules]]
paths = ["fixtures/policy/**"]
rules = ["bifrost.java-codeql-quality.undocumented-*"]
reason = "Policy fixtures are deliberately minimal test inputs."
```

The service reads the file from the code it checks, so `slopcop check`, the
MCP `check` tool and pull request checks all use the same scope, and a change
to it is reviewed like any other code. Unlike `.bifrostignore`, it changes only
what SlopCop checks, never what Bifrost analyzes elsewhere. `.bifrostignore`
still applies first; `[check]` only removes more.

- `exclude` keeps the files it matches from every rule.
- An `exclude_rules` entry drops only its rules' findings on its paths. Every
  other rule keeps checking those files.
- An exclusion is never silent. Every report lists each scope with its reason,
  `--verbose` adds the changed files and rules each one resolved to, and the
  summary counts the excluded files and `(file, rule)` pairs. In `json` they
  are `exclusions` and `summary.excludedFiles`/`excludedPairs`; in `sarif`,
  `properties["slopcop.exclusions"]`.
- A rule that matches no rule the check runs, such as one that was renamed, is
  a warning.
- A change that touches only excluded files says `Nothing checked` instead of
  `No new findings` (`summary.onlyExcluded`); it still exits `0`.
- A `[check]` the service cannot read fails the check with
  `check_config_invalid` rather than check nothing.

## Alternatives

- **A comment at the code** ignores a finding for everyone who checks the
  repository; see [Ignore in the code](#ignore-in-the-code).
- **Pre-existing findings** need no ignore: they never fail a check and are not
  listed. See [Read a report](/checks/read-a-report/#new-and-existing-findings).
- **A whole rule** cannot be turned off from the CLI. If one is noisy for your
  codebase, tell us on [Discord](https://discord.gg/KVk8n5sENg) or through the
  web app.
- **Inconclusive rules** are not findings and need nothing from you.
