---
title: Read a report
description: "What a SlopCop report claims and what it does not. Status, certainty, new versus existing findings, incomplete and inconclusive checks, omitted findings, and empty changes."
sidebar:
  order: 2
---

A report answers two separate questions. **Is each listed finding real?** That
is its certainty. **Did anything go unchecked?** That is the status, the
`incomplete` list, and `failure`. An incomplete check does not make the
findings it reported doubtful, and a definite finding does not make the check
complete. Never read "no findings" as "nothing wrong" unless the check also
completed.

## Status

| Status | Meaning |
| --- | --- |
| `clean` | Every selected rule completed for the files the change writes (an inconclusive one counts as completed), and no new finding crossed `--fail-on`. The only status that supports "nothing found", and only for those rules, files, and lines. Exit `0`. |
| `findings` | A new finding crossed `--fail-on`. Other rules may still be incomplete. Exit `1`. |
| `incomplete` | No new finding crossed `--fail-on`, but some rule could not answer for the changed files, or the analysis failed. Neither a pass nor a fault in the code. Exit `2`. |
| `pending` | Not a result yet: the check was still running when `--timeout` ran out. Run the same check again. Exit `3`. |
| `failed` | The service refused or could not run the check. Carries a `code`, `message`, and `remedy`. Exit `4` when only a person can clear it, `2` otherwise. |
| `skipped` | The CLI sent nothing because `--if-enabled` found the repository not active. Carries `code: repository_not_enabled`, the repository, and its state. Exit `0`; this is not a check result. |

The CLI merges static and review statuses. Top-level JSON `status` matches
the exit code; `staticStatus` keeps the service status when it differs.
Review details are in `review`, including each reviewer, unavailable family,
error, and log path. Exit `0` can include a static skip or empty review;
inspect those statuses before calling the change checked.

## New and existing findings

The static half fails on findings the change introduced or touched.
Review findings are model judgments about the captured change. The service
marks a finding new when it is in a file the change writes and its lines meet a
line the change added, or the line beside one it removed. Findings that were
already in the code are counted (`summary.existingFindings`, and `N already in
the code` in the human summary), never fail the check, and are not listed. A
change that introduces nothing exits `0`, however many findings the repository
already holds.

In `json`, each listed finding has `new: true` and `summary.newFindings` counts
them. In `sarif`, each result's `baselineState` is `new`.

## Certainty

| Certainty | Meaning |
| --- | --- |
| `definite` | Bifrost proved the pattern or relation holds at this location. Fix it. |
| `possible` | The match depends on something Bifrost could not settle: an ambiguous receiver or dispatch, name-based resolution, several candidate declarations, or a "may" relation. It can be a false positive: read the code before changing it. |
| `heuristic` | A text-pattern match, such as a secret-like string. Confirm it the same way. |

Every finding carries an `id`: an `ID:` line in `human`, `id` in `json`,
`properties["slopcop.findingId"]` in `sarif`. It is what
[`slopcop ignore`](/checks/false-positives/) takes.

## Incomplete checks

`incomplete` lists the rules that could not answer for the changed files, each
with a `reason`, and for `unsupported` and `other` a `detail` that `human`
prints after the reason and `sarif` carries in `properties["slopcop.detail"]`:

| Reason | Meaning | What to do |
| --- | --- | --- |
| `unreliable` | Bifrost judged the run unreliable, for example evaluated with a scope file it could not load. The findings it did report keep their certainty. | Report the policy ids. Rerunning gives the same answer, so do not loop on it, and do not rewrite working code only to clear it. |
| `timed_out` | The rule ran out of time and has no verdict. | Run the check again once. A smaller change may finish. |
| `base_comparison_failed` | The change could not be compared with its base commit, so findings that were already there may be reported as new. The risk is extra findings, not missed ones. | Check that the change really introduced each finding before fixing it. |
| `unsupported` | Bifrost could not evaluate the rule here because the analysis it needs is not available for this code. `detail` names what it lacked, such as `type=query_domain domain=execution_scope capability=seed_scope reason=query_source_not_scope_aware`. A service from before this code says `other`. | Report the policy id with its `detail`. Rerunning gives the same answer, so do not loop on it, and do not rewrite working code only to clear it. |
| `other` | It did not complete for a reason the service does not classify. `detail`, when present, gives Bifrost's completion type and reason codes, such as `type=failed reasons=source_read_failed`. | Report it, with its `detail`. |

A report with incomplete checks exits `2` unless a new finding crossed
`--fail-on`, in which case it exits `1`: the findings come first.

## Inconclusive checks

An inconclusive rule ran but could not prove there is nothing more to find,
typically at a library boundary or in code the analysis cannot fully model.
Complete proof is rare in static analysis, so this is the norm rather than a
gap, and it never changes the status or the exit code.

Each report still says where they are, so a reviewer can judge what a `clean`
result is worth. `human` prints one `Inconclusive:` line with the count and how
many concern only files the change does not write; `--verbose` names each rule.
`json` lists them in `inconclusive`, each with `policyId`, `reason`, and
`elsewhere` (`true` when it concerns only files the change does not write),
beside the `summary.inconclusive` count. `sarif` adds a `note` notification per
rule. The service names at most fifty; the counts cover the rest.

Do nothing about them: do not rerun, rewrite code, or ignore or disable a
policy because of them. Name them only when someone asks how much a `clean`
result covers.

## Other bounds

- **`summary.omittedFindings`**: new findings left out because one rule found
  more than it may report; the most severe are listed. They never change the
  status, and a large count is almost always one noisy policy over a large new
  file. Findings already in the code that were left out are counted in
  `existingFindings`, not here.
- **`failure`**: the analysis as a whole did not complete, and the report
  makes no claim about the code, even if it lists findings. `applied` is
  `false` whenever the analysis did not complete.
- **`repositoryAdded`**: `true` only when this check carried an approved add,
  as `slopcop enable` or `--add-repository` does. A check never adds a
  repository by itself. See [Repositories](/account/repositories/).
- **`emptyChange`**: `true` only when the patch was empty because the working
  tree matches the base. Nothing was checked; rerun with `--base` if you meant
  to check committed work.
- **`skillUpdated`**: present only when this run installed or replaced the
  [agent skill](/agents/skill/). An agent's loaded copy of the instructions is
  then out of date.

## The human summary line

```text
1 new finding across 1 file(s); 72 already in the code | Analysis complete
```

The first clause is what the change introduced and where; the second is the
existing count; after the bar, whether the analysis completed, followed by any
omitted or incomplete rules and the `Inconclusive:` line.
