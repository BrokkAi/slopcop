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
| `clean` | Every selected rule completed for the files the change writes (an inconclusive one counts as completed), and the change has no new finding. The only status that supports "nothing found", and only for those rules, files, and lines. Exit `0`. |
| `findings` | The change has new findings. Other rules may still be incomplete. Exit `1` when one crossed `--fail-on`; below the threshold, or with `--fail-on never`, the exit is what it would be without them: `0`, or `2` when a rule did not complete. |
| `incomplete` | The change has no new finding, but some rule could not answer for the changed files, or the analysis failed. Neither a pass nor a fault in the code. Exit `2`. |
| `pending` | Not a result yet: the check was still running when `--timeout` ran out. Run the same check again. Exit `3`. |
| `failed` | The service refused or could not run the check. Carries a `code`, `message`, and `remedy`. Exit `4` when only a person can clear it, `2` otherwise. |
| `skipped` | The CLI sent nothing because `--if-enabled` found the repository not active. Carries `code: repository_not_enabled`, the repository, and its state. Exit `0`; this is not a check result. |

The CLI merges static and review statuses. Top-level JSON `status` matches
the exit code, except that new findings below `--fail-on` (or with
`--fail-on never`) still read `findings`; `staticStatus` keeps the static
half's status when it differs.
Review details are in `review`, including each configured reviewer, its error
if it could not run, and its log path. Exit `0` can include a static skip or empty review;
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

A check lists only `definite` findings unless run with `--certainty all`. The
others are counted, in `human` on the summary line ("2 less certain not
shown"), in `json` as `summary.lessCertainFindings`, and in `sarif` as
`properties["slopcop.lessCertainFindings"]`; they do not count in
`newFindings` and do not change the status. Under `--certainty all` the
count is `0`, and `json` and `sarif` still carry it, so you can tell the
filter took effect.

Every finding carries an `id`: an `ID:` line in `human`, `id` in `json`,
`properties["slopcop.findingId"]` in `sarif`. It is what
[`slopcop ignore`](/checks/false-positives/) takes.

## Dependency advisories

When a change adds or upgrades a dependency to a version with a known
advisory, the service reports it as a finding on the changed manifest or
lockfile with a `dependency` object. The service reads the manifests and
lockfiles and looks the advisory up; the CLI never parses them, runs a package
manager, or contacts an advisory database. `human` shows a compact block
under the finding:

```text
  [error] slopcop.dependencies.known-vulnerability: lodash 4.17.20 has a known vulnerability
    Location: package-lock.json:12
    Certainty: definite
    ID: 7d2a1c3e-…
    Package: npm lodash@4.17.20 (direct, in package.json)
    Advisory: GHSA-29mw-wpgm-hmr9 (CVE-2020-28500) https://osv.dev/vulnerability/GHSA-29mw-wpgm-hmr9
    Fixed in: 4.17.21
    Upgrade: npm install lodash@4.17.21 --save-exact
```

A transitive dependency adds a `Chain:` line from the direct dependency to the
vulnerable package. `json` carries the object as `dependency` on the finding,
and `sarif` as `properties["slopcop.dependency"]`, with `ecosystem`, `package`,
`version`, `direct`, `manifestPath`, `dependencyChain`, `advisoryId`,
`aliases`, `sourceUrl`, `summary`, `fixedVersions`, `upgradeCommand` and
`databaseModified`, each present when the service sent it. The CLI keeps only
an `https` `sourceUrl`, cuts each string and list to a bound, and then sets
`truncated`; a chain cut in the middle keeps its last link and counts the
rest in `dependencyChainOmitted`. Upgrade to a fixed version, or
[`slopcop ignore`](/checks/false-positives/) the finding's id like any other.

## Incomplete checks

`incomplete` lists the rules that could not answer for the changed files, each
with a `reason`, and often a `detail` that `human` prints after the reason and
`sarif` carries in `properties["slopcop.detail"]`. A rule whose cause was the
whole analyzer run's also has `scope: "run"`:

| Reason | Meaning | What to do |
| --- | --- | --- |
| `unreliable` | Bifrost judged the run unreliable, for example evaluated with a scope file it could not load. `detail`, when present, names the cause, such as `type=incomplete reasons=analyzer_process_failed exit_code=137`. The findings it did report keep their certainty. | Report the policy ids. Rerunning gives the same answer, so do not loop on it, and do not rewrite working code only to clear it. |
| `timed_out` | The rule ran out of time and has no verdict. | Run the check again once. A smaller change may finish. |
| `base_comparison_failed` | The change could not be compared with its base commit, so findings that were already there may be reported as new. The risk is extra findings, not missed ones. | Check that the change really introduced each finding before fixing it. |
| `unsupported` | Bifrost could not evaluate the rule here because the analysis it needs is not available for this code. `detail` names what it lacked, such as `type=query_domain domain=execution_scope capability=seed_scope reason=query_source_not_scope_aware`. A service from before this code says `other`. | Report the policy id with its `detail`. Rerunning gives the same answer, so do not loop on it, and do not rewrite working code only to clear it. |
| `other` | It did not complete for a reason the service does not classify. `detail`, when present, gives Bifrost's completion type and reason codes, such as `type=failed reasons=source_read_failed`. | Report it, with its `detail`. |

The service names at most fifty incomplete rules; `summary.incompletePolicies`
counts them all. When it cut the list, `json` says how many it left out in
`incompleteUnlisted`, `sarif` in a `warning` notification and the run's
`properties["slopcop.incompleteUnlisted"]`, and `human` ends the
`Incomplete:` line with `and N more`. `json` also groups the named rules by
reason and detail in `incompleteReasons`, most common first, and `human` says a
reason that most named rules share once, before the list:

```text
Incomplete (48 of 50 named: unreliable): bifrost.correctness.a, …, bifrost.correctness.z (timed_out), and 157 more
```

Many rules with one shared reason usually means one cause in the run, not one
per rule: report that reason once, with the count.

When an analyzer run as a whole failed, the service names each cause once in
`runFailures`, each with `reason`, `detail` when known, and `policies`: how many
rules it left incomplete, including those past the fifty named. `json` passes
the list through as sent, and `sarif` adds a `warning` notification per cause
with `slopcop.runFailure` and the run property `slopcop.runFailures`. `human`
prints one line per cause before the `Incomplete:` line, where the rules it
covers carry only their ids:

```text
Analyzer run failed (unreliable: type=incomplete reasons=analyzer_process_failed exit_code=137): 207 check(s) did not complete
```

Report each run failure once, with its detail. It is a fault in the run, not in
the change, so do not rewrite code to clear it.

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
`json` lists them in `inconclusive`, each with `policyId`, `reason`, `detail`
when the service sent one, and `elsewhere` (`true` when it concerns only files
the change does not write), beside the `summary.inconclusive` count. `sarif`
adds a `note` notification per rule. The service names at most fifty; the
counts cover the rest, and `json`'s `inconclusiveUnlisted` says how many it
left out when it cut the list.

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
- **`omittedFiles`**: JSON data and binary artifacts omitted from upload and
  local review by the 500,000-byte size filter. Each entry gives the path,
  larger base/current size in bytes, and reason. Those contents were not
  analyzed; a passing check covers the retained scope. Repeatable `--include`
  paths or globs opt particular files back in. See [Run a check](/checks/run-a-check/).
- **`emptyChange`**: the patch has no in-scope changes. If the size filter
  omitted every changed file, the result is `skipped` with no analysis or push
  approval. Otherwise the working tree matches the base; rerun with `--base`
  if you meant to check committed work.
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
