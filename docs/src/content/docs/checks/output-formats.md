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

The default. One block per new finding, then a summary line, then any omitted
new findings, incomplete rules, and the `Inconclusive:` count. Each block has
the finding's rule and title, a `Message:` line when the analyzer observed more
than the title says, its location (with the column when the service sent the
span), and a `Code:` line quoting a short one-line span from the files the
check uploaded. It has an `ID:` line when the service names one.

```text
New findings
  [warning] bifrost.javascript-codeql-quality.misspelled-identifier: an identifier may be misspelled
    Location: client/lib/feed.ts:16:11
    Code: `segmnet`
    Certainty: possible
    ID: 0f8fad5b-d9cb-469f-a165-70867728950e
    Word: segmnet; add it to the workspace dictionary with `slopcop dictionary add 0f8fad5b-d9cb-469f-a165-70867728950e`

  [warning] bifrost.maintainability.cognitive-complexity: Cognitive complexity 32: renderFeed
    Message: Bifrost computed a score of 32; the quick-review threshold is 15.
    Location: client/lib/feed.ts:40:1
    Certainty: definite
    ID: 7c9e6679-7425-40de-944b-e07fc1f90ae7

2 new findings across 1 file(s); 72 already in the code | Analysis complete
Inconclusive: 1 check(s); they never fail the check.
```

The title, message and quoted code come from the analyzed repository; the
CLI prints them with control characters replaced.

A static-only failed check prints nothing on stdout; stderr starts with
`slopcop: <code>:` followed by the service's message and remedy, verbatim, and
any install or top-up link. `--verbose` names each inconclusive rule and up to five files it concerns.

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
  "approved": false,
  "baseCommit": "0123456789abcdef0123456789abcdef01234567",
  "summary": {
    "files": 1,
    "findings": 74,
    "newFindings": 2,
    "existingFindings": 72,
    "incompletePolicies": 0,
    "incompletePolicyIds": [],
    "inconclusive": 1,
    "omittedFindings": 0,
    "failOn": "warning"
  },
  "findings": [
    {
      "id": "0f8fad5b-d9cb-469f-a165-70867728950e",
      "policyId": "bifrost.javascript-codeql-quality.misspelled-identifier",
      "severity": "warning",
      "certainty": "possible",
      "path": "client/lib/feed.ts",
      "startLine": 16,
      "endLine": 16,
      "startColumn": 11,
      "endColumn": 18,
      "title": "an identifier may be misspelled",
      "new": true,
      "suggestedWord": "segmnet"
    },
    {
      "id": "7c9e6679-7425-40de-944b-e07fc1f90ae7",
      "policyId": "bifrost.maintainability.cognitive-complexity",
      "severity": "warning",
      "certainty": "definite",
      "path": "client/lib/feed.ts",
      "startLine": 40,
      "endLine": 88,
      "startColumn": 1,
      "endColumn": 2,
      "title": "Cognitive complexity 32: renderFeed",
      "message": "Bifrost computed a score of 32; the quick-review threshold is 15.",
      "new": true
    }
  ],
  "incomplete": [],
  "inconclusive": [
    { "policyId": "bifrost.correctness.resource-lifecycle", "reason": "inconclusive", "detail": "type=inconclusive reasons=partial_discovery", "files": ["src/pool.rs"], "elsewhere": true }
  ]
}
```

| Field | Meaning |
| --- | --- |
| `checkId` | the check that answered; absent from an older service |
| `staticStatus` | the static half's status when it differs from merged `status` |
| `exitReason` | present only when the check exits `1`: `static_findings` (new static findings crossed `--fail-on`), `review_findings` (only the local review's findings failed it; `summary.findings` can then be `0`), or `static_and_review_findings`. Also in every other `json` document that can exit `1`, and in `sarif` as `properties["slopcop.exitReason"]` |
| `review` | `status`, `reviewers`, `findings`, `error`, `logDir`, `intentSource`, `baseTree`, `tree`, and `reused`. `findings` is what the reviewers reported, parsed from each one's reply: `id` (`rv-` and twelve hex digits, which [`slopcop override <rv-id>`](/checks/false-positives/#override-a-review-finding) takes), `reviewer`, `priority` (`P0` to `P3`, absent when untagged), `path`, `startLine` and `endLine` when the reviewer named a location, `title`, and `message`. A reply without a tagged finding is one finding with `malformed: true`, holding the whole reply with no priority or location; read the reviewer's `text`. `overridden`, present when it has any, lists the reported findings left out because they match a finding overridden with `slopcop override <rv-id>`: each with the fields of a finding, `overrideId`, the id of the override it matched, and `match`: `text` for the same text at the same place, `words` for a rewording at the same place, worth reading to be sure it is the same point; they are not in `findings` and do not fail the check. `slopcop review show --format json` prints the latest review again from the reviewer logs, with a top-level [`reviewOverride`](/reference/cli/#slopcop-review-show) when `slopcop override` accepted it; `approved` stays `false` there |
| `reviewSkipped` | present when the local review did not run, and why: `noReviewers` (no mode set and no reviewers configured), `ci` (no mode set in a CI job), `staticMode` (static mode chosen by flag or config), or `repositoryInactive` (the check was refused as `repository_disabled` or `active_repository_limit_reached` before the review started). A static-only `clean` is not a reviewed change |
| `approved` | whether the checked tree got push approval; always present |
| `advisoryFindings` | present when the change has new findings below the push level: how many. They do not keep `approved` from being `true`, but a [pull request check fails on them](/checks/exit-codes/#the-push-gate-and-pull-request-checks). In `sarif`, `properties["slopcop.advisoryFindings"]` on the first run |
| `status` | merged result: `clean`, `findings`, `incomplete`, `pending`, `failed`, or a static-only `skipped`. It is `findings` whenever the change has new findings, whatever `--fail-on` is; `--fail-on` decides only the exit code |
| `baseCommit` | the commit the patch was applied to |
| `summary` | `newFindings`, `existingFindings`, `omittedFindings`, `lessCertainFindings`, `incompletePolicies`, `incompletePolicyIds`, `inconclusive`, `remappedIgnores`, and `reviewFindings` when a review ran, which counts `review.findings` (the static counts never include them); `lessCertainFindings` counts the new `possible` and `heuristic` findings left out because `--certainty` was not `all`, is `0` under `--certainty all`, and is absent only when an older service did not send it; and, when the check met a `slopcop: ignore` comment, `inlineSuppressed` and `directiveWarnings` |
| `findings` | each new finding with its `id`, `new`, `policyId`, `severity`, `certainty`, `path`, `startLine` and `endLine`, and `title`, the rule's sentence. `startColumn` and `endColumn` are the analyzer's span when it gave one: 1-based, with `endColumn` the column just past it on `endLine`. `message` is what the analyzer observed beyond the title, such as a score and its threshold or the protocol step a typestate rule found broken: one line of at most 500 characters, absent when it would repeat the title. `previouslyIgnored` is the id of an ignored finding it matches, and a finding with it is listed even when it is not new; `suggestedWord` is a misspelled-identifier finding's flagged word. A possible credential carries no columns or message |
| `incomplete` | rules that did not complete, each with `policyId`, `reason`, `detail` when the service sent one, `diagnostic` (`message`, and `line` and `column` when known) for a repository rule file that did not load, compile or run, and `scope: "run"` when the whole analyzer run shared the cause; at most fifty |
| `runFailures` | present when an analyzer run as a whole failed: each cause once, with `reason`, `detail`, and `policies`, the number of rules it left incomplete |
| `incompleteUnlisted` | present when the service cut `incomplete`: how many of `summary.incompletePolicies` it does not name |
| `incompleteReasons` | present when a rule did not complete: the named rules grouped by `reason` and `detail`, each with a `policies` count, most common first |
| `inconclusive` | rules that ran but could not prove completeness, each with `policyId`, `reason`, `detail` when sent, `files` (the repository paths the check concerns) when the analyzer named them all, `elsewhere` |
| `inconclusiveUnlisted` | present when the service cut `inconclusive`: how many of `summary.inconclusive` it does not name |
| `inlineSuppressed` | present when a [`slopcop: ignore` comment](/checks/false-positives/#ignore-in-the-code) ignored a finding: each one with the fields of a finding except `id`, and `directive` with the comment's `line`, `blanket`, `policies` and `reason`; at most 300 |
| `directiveWarnings` | present when a `slopcop: ignore` comment ignored nothing: each with `kind` (`unused`, `missing_reason`, `malformed`), `path`, `line`, `policies`, `detail` for a malformed one, and `new`; at most 300 |
| `failure` | present when the analysis as a whole did not complete |
| `repositoryAdded` | `true` when this check carried an approved add and the service added the repository to the workspace |
| `addedToWorkspace` | with `repositoryAdded`: `id` and `name` of the workspace the repository was added to, `source` (`flag`, `env`, `mapping` or `login`) saying how it was chosen, and `change`, how a person checks it in another workspace instead |
| `emptyChange` | `true` when the patch was empty |
| `omittedFiles` | files omitted from both upload and local review by the size filter or because `.bifrostignore` or `[check] exclude` keeps them out of checks, each with `path`, `bytes` (the larger base/current size), and `reason` (`large-json`, `large-binary` or `excluded`); their contents were not analyzed |
| `skillUpdated` | present when this run installed or updated the [agent skill](/agents/skill/): each entry's `dir`, `from`, `to`, `changed`, and `backup` |
| `repositorySize` | the service's measure of the check's checkout, as it sent it: `files`, `bytes`, `maxFiles`, `maxBytes`, `excludedFiles`, `excludedBytes`, and `nearLimit`; absent from an older service |
| `sizeBreakdown` | present when `repositorySize.nearLimit` is true: the base tree measured locally, with `base`, `files`, `bytes`, `excludedFiles`, `excludedBytes` (what `.bifrostignore` and `[check] exclude` already leave out), `rankedBy` (`bytes` or `files`), `largest` (at most five paths, each with `path`, `directory`, `files`, `bytes` and the `[check] exclude` `pattern` that leaves it out), the config's current `exclude` list and `hasCheck`, and `configError` when `.slopcop/config.toml` does not parse |
| `credits` | what funds the workspace's next checks, for a workspace charged for checks: `freeChecksLeft` and `freeChecksPerPeriod` in the current `period` (`day` or `month`, next at `resetsAt`), `availableCredits`, `checkPriceCredits`, `checksLeft` (free checks and the checks the credits pay for), `low`, and `topUpUrl`; absent for a workspace never charged and from an older service |
| `orgRules` | present when the check ran shared rules from the owner's [`.slopcop` repository](/checks/repository-rules/#share-rules-across-an-organization): `repository`, the `commit` they were read at, and each rule file's path in `files` |

Failure, pending, skipped, and AI-only JSON also include `approved` and any
`omittedFiles`. If the size filter omits every changed file, the result is `skipped`
with no approval or analysis. The first SARIF run has `slopcop.approved` in its
properties; `slopcop.omittedFiles` lists omissions on each run. A local review's
`tree` identifies the complete snapshot, while optional `diffTree` identifies
the filtered scope actually reviewed.

A failed check prints `{"status": "failed", "code": …, "message": …,
"remedy": …, "retryable": …}`, plus `installUrl` or `topUpUrl` when the service
sent one, and `sizeBreakdown` for `repository_too_large`. A check still running at `--timeout` prints `{"status": "pending",
"retryAfter": 5, "message": …, "remedy": …}`. `--if-enabled` with a disabled
repository prints `{"status": "skipped", "code": "repository_not_enabled",
"state": …, "repository": …, "message": …}` and exits `0`, because it sent
nothing.

[Read a report](/checks/read-a-report/) explains what each field claims.

## sarif

```console
slopcop check --format sarif > slopcop.sarif
```

A SARIF 2.1.0 log with a second run, `slopcop-review`, containing
one result per reviewer finding and failures as notifications. Each review
result's `ruleId` is the reviewer, its `level` follows the priority (`error`
for `P0` and `P1`, `note` for `P3`, otherwise `warning`), its
`properties["slopcop.priority"]` is the priority,
`properties["slopcop.findingId"]` is its `rv-` id, and it has a location when
the reviewer named one. A reviewer finding left out because it matches an
overridden finding is a result with an `external` suppression and the
override's id in `properties["slopcop.overrideId"]`.
Each static result's `message.text` is the finding's title, then its
`message` on a second line when it has one, and its region carries
`startColumn` and `endColumn` when the service sent the span.
The static results carry `baselineState` `new` and the finding id
in `properties["slopcop.findingId"]`, and the id of an ignored finding it
matches in `properties["slopcop.previouslyIgnored"]`, counted in the run's
`slopcop.remappedIgnores`. Each incomplete rule is a `warning` notification
with `slopcop.reason` and, when sent, `slopcop.detail`; a repository rule
file that did not load also has `slopcop.ruleMessage`, and `slopcop.ruleLine`
and `slopcop.ruleColumn` when known, with the message in its text; when the service cut
the list, one more `warning` notification counts the rest in
`slopcop.unlisted`, and the run carries `slopcop.incompleteUnlisted` and
`slopcop.incompleteReasons`. Each run failure is a `warning` notification with
`slopcop.runFailure`, `slopcop.reason`, `slopcop.detail`, and
`slopcop.policies`, and the run carries `slopcop.runFailures`. Inconclusive rules appear as `note`
notifications with `slopcop.inconclusive`, `slopcop.elsewhere`, and, when sent,
`slopcop.detail` properties, and one location per file the check concerns
when the service named them, and the run carries `slopcop.inconclusiveUnlisted`
when that list was cut;
a finding a `slopcop: ignore` comment ignored is a result with an `inSource`
suppression whose `justification` is the comment's reason, and each comment
that ignored nothing is a `warning` notification with
`slopcop.directiveWarning`; the run counts both in `slopcop.inlineSuppressed`
and `slopcop.directiveWarnings`;
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
