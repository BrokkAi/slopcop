---
title: Output formats
description: "The human, JSON, and SARIF renderings of a check report, with the fields each carries and how failures and pending checks appear in each."
sidebar:
  order: 4
---

The hosted service and local review answer separately; the CLI merges
and renders their results. `--format` only
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

A static-only failed check prints nothing on stdout; stderr starts with
`slopcop: <code>:` followed by the service's message and remedy, verbatim, and
any install or top-up link. `--verbose` names each inconclusive rule.

After the static report, human output includes a Review section with each
reviewer's verdict, text, log path, and intent source.
Review progress goes to stderr. The prompt carries the captured diff. Reviewers read repository content,
which is untrusted. Claude and DeepSeek reviewers may only read and search
files; Slopcop refuses their requests to run commands or change files. Codex
reviewers run in Codex's read-only sandbox.

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
| `staticStatus` | the static half's status when it differs from merged `status` |
| `review` | `status`, `reviewers`, `error`, `logDir`, `intentSource`, `baseTree`, `tree`, and `reused` |
| `approved` | whether the checked tree got push approval; always present |
| `status` | merged result: `clean`, `findings`, `incomplete`, `pending`, `failed`, or a static-only `skipped`. It is `findings` whenever the change has new findings, whatever `--fail-on` is; `--fail-on` decides only the exit code |
| `baseCommit` | the commit the patch was applied to |
| `applied` | whether the analysis completed; `false` whenever it did not |
| `summary` | `newFindings`, `existingFindings`, `omittedFindings`, `lessCertainFindings`, `incompletePolicies`, `incompletePolicyIds`, `inconclusive`, `remappedIgnores`; `lessCertainFindings` counts the new `possible` and `heuristic` findings left out because `--certainty` was not `all`, is `0` under `--certainty all`, and is absent only when an older service did not send it |
| `findings` | each new finding with its `id`, `new`, policy, severity, certainty, location, and message, and `previouslyIgnored` with the id of an ignored finding it matches; a finding with `previouslyIgnored` is listed even when it is not new |
| `incomplete` | rules that did not complete, each with `policyId`, `reason`, `detail` when the service sent one, and `scope: "run"` when the whole analyzer run shared the cause; at most fifty |
| `runFailures` | present when an analyzer run as a whole failed: each cause once, with `reason`, `detail`, and `policies`, the number of rules it left incomplete |
| `incompleteUnlisted` | present when the service cut `incomplete`: how many of `summary.incompletePolicies` it does not name |
| `incompleteReasons` | present when a rule did not complete: the named rules grouped by `reason` and `detail`, each with a `policies` count, most common first |
| `inconclusive` | rules that ran but could not prove completeness, each with `policyId`, `reason`, `detail` when sent, `elsewhere` |
| `inconclusiveUnlisted` | present when the service cut `inconclusive`: how many of `summary.inconclusive` it does not name |
| `failure` | present when the analysis as a whole did not complete |
| `repositoryAdded` | `true` when this check carried an approved add and the service added the repository to the workspace |
| `emptyChange` | `true` when the patch was empty |
| `skillUpdated` | present when this run installed or updated the [agent skill](/agents/skill/): each entry's `dir`, `from`, `to`, `changed`, and `backup` |

Failure, pending, skipped, and AI-only JSON also include `approved`. The first
SARIF run has `slopcop.approved` in its properties.

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

A SARIF 2.1.0 log with a second run, `slopcop-review`, containing
reviewer findings as location-free results and failures as notifications.
The static results carry `baselineState` `new` and the finding id
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
an empty change sets `properties["slopcop.emptyChange"]`; a skill update sits in
`properties["slopcop.skillUpdated"]`. The first run's
`properties["slopcop.approved"]` says whether the checked tree got push
approval.

The static run's `slopcop.status` names the result the same way: `findings`
whenever the change has new findings, even when `--fail-on` lets the check exit
`0`.

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
