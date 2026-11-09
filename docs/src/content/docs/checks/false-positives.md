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

## Alternatives

- **Pre-existing findings** need no ignore: they never fail a check and are not
  listed. See [Read a report](/checks/read-a-report/#new-and-existing-findings).
- **A whole rule** cannot be turned off from the CLI. If one is noisy for your
  codebase, tell us on [Discord](https://discord.gg/KVk8n5sENg) or through the
  web app.
- **Inconclusive rules** are not findings and need nothing from you.
