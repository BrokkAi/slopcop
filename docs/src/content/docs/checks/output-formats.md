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
| `status` | `clean`, `findings`, `incomplete`, `pending`, or `failed` |
| `baseCommit` | the commit the patch was applied to |
| `applied` | whether the analysis completed; `false` whenever it did not |
| `summary` | `newFindings`, `existingFindings`, `omittedFindings`, `inconclusive` |
| `findings` | each new finding with its `id`, `new`, policy, severity, certainty, location, and message |
| `incomplete` | rules that did not complete, each with a `reason` |
| `inconclusive` | rules that ran but could not prove completeness, each with `policyId`, `reason`, `elsewhere` |
| `failure` | present when the analysis as a whole did not complete |
| `repositoryAdded` | `true` when this check added the repository to the workspace |
| `emptyChange` | `true` when the patch was empty |
| `skillUpdated` | present when this run installed or updated the [agent skill](/agents/skill/): each entry's `dir`, `from`, `to`, `changed`, and `backup` |

A failed check prints `{"status": "failed", "code": …, "message": …,
"remedy": …, "retryable": …}`, plus `installUrl` or `topUpUrl` when the service
sent one. A check still running at `--timeout` prints `{"status": "pending",
"retryAfter": 5, "message": …, "remedy": …}`.

[Read a report](/checks/read-a-report/) explains what each field claims.

## sarif

```console
slopcop check --format sarif > slopcop.sarif
```

A SARIF 2.1.0 log whose results carry `baselineState` `new` and the finding id
in `properties["slopcop.findingId"]`. Inconclusive rules appear as `note`
notifications with `slopcop.inconclusive` and `slopcop.elsewhere` properties;
an empty change sets `properties["slopcop.emptyChange"]`; a skill update sits in
`properties["slopcop.skillUpdated"]`.

A failed check prints a run with `slopcop.status` `failed`, whose invocation is
unsuccessful and carries an `error` notification whose descriptor id is the
code. A pending check prints a run with `slopcop.status` `pending`.

SARIF is what GitHub code scanning and most CI annotators read; see
[Gate a change in CI](/checks/ci/).

## Account commands

`whoami`, `status`, `workspace list`, and `repo list` take `--format human|json`,
as do `workspace use` and `repo enable|disable` for their answer. See the
[CLI reference](/reference/cli/).
