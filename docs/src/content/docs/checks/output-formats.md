---
title: Output formats
description: "The human, JSON, and SARIF renderings of a check report, with the fields each carries and how failures and pending checks appear in each."
sidebar:
  order: 4
---

The service answers structured data; the CLI renders it. `--format` only
changes the rendering, never the request, so the service answers the same check
whichever is used, and running the same check again in another format picks up
the same analysis.

## human

The default. One block per new finding, with an `ID:` line when the service
names one, then a summary line, then any omitted new findings, incomplete
rules, and the `Inconclusive:` count.

```text
src/main/java/orders/ExportServlet.java:48  request parameter reaches SQL text  definite · error
  ID: 0f8fad5b-d9cb-469f-a165-70867728950e
  bifrost.security.java.servlet-parameter-to-jdbc: req.getParameter("q") reaches
  Statement.executeQuery(String) through OrderQuery.build (OrderQuery.java:21).

1 new finding across 1 file(s); 72 already in the code | Analysis complete
Inconclusive: 3 (2 elsewhere)
```

A failed check prints nothing on stdout; stderr starts with
`slopcop: <code>:` followed by the service's message and remedy, verbatim, and
any install or top-up link. `--verbose` names each inconclusive rule.

## json

```console
slopcop check --format json
```

```json
{
  "checkId": "6f1c2a7e-3b4d-4e5f-8a9b-0c1d2e3f4a5b",
  "status": "findings",
  "baseCommit": "0123456789abcdef0123456789abcdef01234567",
  "applied": true,
  "summary": {
    "newFindings": 1,
    "existingFindings": 72,
    "omittedFindings": 0,
    "inconclusive": 3
  },
  "findings": [
    {
      "id": "0f8fad5b-d9cb-469f-a165-70867728950e",
      "new": true,
      "policyId": "bifrost.security.java.servlet-parameter-to-jdbc",
      "severity": "error",
      "certainty": "definite",
      "path": "src/main/java/orders/ExportServlet.java",
      "line": 48,
      "message": "req.getParameter(\"q\") reaches Statement.executeQuery(String) through OrderQuery.build (OrderQuery.java:21)."
    }
  ],
  "incomplete": [],
  "inconclusive": [
    { "policyId": "bifrost.correctness.resource-lifecycle", "reason": "library boundary", "elsewhere": true }
  ]
}
```

| Field | Meaning |
| --- | --- |
| `status` | `clean`, `findings`, `incomplete`, `pending`, `failed`, or, for `--if-enabled` with an inactive repository, `skipped` |
| `baseCommit` | the commit the patch was applied to |
| `applied` | whether the analysis completed; `false` whenever it did not |
| `summary` | `newFindings`, `existingFindings`, `omittedFindings`, `incompletePolicies`, `incompletePolicyIds`, `inconclusive`, `remappedIgnores`, and, when the check met a `slopcop: ignore` comment, `inlineSuppressed` and `directiveWarnings` |
| `findings` | each new finding with its `id`, `new`, policy, severity, certainty, location, and message, and `previouslyIgnored` with the id of an ignored finding it matches; a finding with `previouslyIgnored` is listed even when it is not new |
| `incomplete` | rules that did not complete, each with `policyId`, `reason`, `detail` when the service sent one, and `scope: "run"` when the whole analyzer run shared the cause; at most fifty |
| `runFailures` | present when an analyzer run as a whole failed: each cause once, with `reason`, `detail`, and `policies`, the number of rules it left incomplete |
| `incompleteUnlisted` | present when the service cut `incomplete`: how many of `summary.incompletePolicies` it does not name |
| `incompleteReasons` | present when a rule did not complete: the named rules grouped by `reason` and `detail`, each with a `policies` count, most common first |
| `inconclusive` | rules that ran but could not prove completeness, each with `policyId`, `reason`, `detail` when sent, `elsewhere` |
| `inconclusiveUnlisted` | present when the service cut `inconclusive`: how many of `summary.inconclusive` it does not name |
| `inlineSuppressed` | present when a [`slopcop: ignore` comment](/checks/false-positives/#ignore-in-the-code) ignored a finding: each one with the fields of a finding except `id`, and `directive` with the comment's `line`, `blanket`, `policies` and `reason`; at most 300 |
| `directiveWarnings` | present when a `slopcop: ignore` comment ignored nothing: each with `kind` (`unused`, `missing_reason`, `malformed`), `path`, `line`, `policies`, `detail` for a malformed one, and `new`; at most 300 |
| `failure` | present when the analysis as a whole did not complete |
| `repositoryAdded` | `true` when this check carried an approved add and the service added the repository to the workspace |
| `emptyChange` | `true` when the patch was empty |
| `skillUpdated` | present when this run installed or updated the [agent skill](/agents/skill/): each entry's `dir`, `from`, `to`, `changed`, and `backup` |

A failed check prints `{"status": "failed", "code": …, "message": …,
"remedy": …, "retryable": …}`, plus `installUrl` or `topUpUrl` when the service
sent one. A check still running at `--timeout` prints `{"status": "pending",
"retryAfter": 5, "message": …, "remedy": …}`. `--if-enabled` with an inactive
repository prints `{"status": "skipped", "code": "repository_not_enabled",
"state": …, "repository": …, "message": …}` and exits `0`, because it sent
nothing.

[Read a report](/checks/read-a-report/) explains what each field claims.

## sarif

```console
slopcop check --format sarif > slopcop.sarif
```

A SARIF 2.1.0 log whose results carry `baselineState` `new` and the finding id
in `properties["slopcop.findingId"]`, and the id of an ignored finding it
matches in `properties["slopcop.previouslyIgnored"]`, counted in the run's
`slopcop.remappedIgnores`. Each incomplete rule is a `warning` notification
with `slopcop.reason` and, when sent, `slopcop.detail`; when the service cut
the list, one more `warning` notification counts the rest in
`slopcop.unlisted`, and the run carries `slopcop.incompleteUnlisted` and
`slopcop.incompleteReasons`. Each run failure is a `warning` notification with
`slopcop.runFailure`, `slopcop.reason`, `slopcop.detail`, and
`slopcop.policies`, and the run carries `slopcop.runFailures`. Inconclusive rules appear as `note`
notifications with `slopcop.inconclusive`, `slopcop.elsewhere`, and, when sent,
`slopcop.detail` properties, and the run carries `slopcop.inconclusiveUnlisted`
when that list was cut;
a finding a `slopcop: ignore` comment ignored is a result with an `inSource`
suppression whose `justification` is the comment's reason, and each comment
that ignored nothing is a `warning` notification with
`slopcop.directiveWarning`; the run counts both in `slopcop.inlineSuppressed`
and `slopcop.directiveWarnings`;
an empty change sets `properties["slopcop.emptyChange"]`; a skill update sits in
`properties["slopcop.skillUpdated"]`.

A failed check prints a run with `slopcop.status` `failed`, whose invocation is
unsuccessful and carries an `error` notification whose descriptor id is the
code. A pending check prints a run with `slopcop.status` `pending`; a skipped
one prints `skipped` with a `note` notification.

SARIF is what GitHub code scanning and most CI annotators read; see
[Gate a change in CI](/checks/ci/).

## Account commands

`whoami`, `status`, `workspace list`, and `repo list` take `--format human|json`,
as do `workspace use` and `repo enable|disable` for their answer. See the
[CLI reference](/reference/cli/).
