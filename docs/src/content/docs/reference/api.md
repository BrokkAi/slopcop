---
title: HTTP API
description: "The routes the slopcop CLI calls on the service, with their request and response shapes. For anyone writing another client."
sidebar:
  order: 4
---

The CLI is the supported client, and its [source](https://github.com/BrokkAi/slopcop)
is the reference for the wire contract. This page summarizes the routes so
another client can be written. Every route takes a bearer token from
`slopcop login`, and with `--workspace` the workspace's id in the
`SlopCop-Workspace` header. The base URL is the service URL, `https://slopcop.com`
by default.

## Check

`POST /v1/check`

```json
{
  "repoUrl": "https://github.com/owner/name",
  "baseCommit": "0123456789abcdef0123456789abcdef01234567",
  "change": "<base64 git patch>",
  "selection": {"files": [".slopcop/policies/rule.rqlp"]},
  "failOn": "finding",
  "addRepository": true
}
```

`addRepository` is sent only after a person confirms adding the repository:
by `slopcop enable` at a terminal, or by `--add-repository` for the workspace
they approved. A check without it never adds a repository; the CLI refuses a
repository the workspace has not added locally, before sending anything.
Run from an agent session, the request also carries an `attribution` object
with the harness, its version, the session id, and each model and effort with
the files it wrote. The service rejects any other field.

The response carries `status`, `baseCommit`, `applied`, `summary`, `findings`
(each with an `id` and `new`, and `previouslyIgnored` naming the ignored
finding's id when the finding plausibly is one whose code changed, counted in
`summary.remappedIgnores`), `incomplete`, `inconclusive`, `failure` when
the analysis did not complete, and `repositoryAdded: true` when the check added
the repository to the workspace. [Output formats](/checks/output-formats/) shows
the `json` rendering, which is this response plus `emptyChange` and
`skillUpdated` added by the CLI.

Every answer about a check carries its `checkId`. While the analysis runs, the
service answers `503` with code `check_running`, a `Retry-After` and the
`checkId`; the client then asks `GET /v1/check/{checkId}` (with `?failOn=` if
it set one), which waits for the same check without the change and answers what
the original request would. Only the token that started the check, in the same
workspace, can read it by id, for 24 hours; anything else answers `404
check_not_found`, and the client sends the change again. Sending the identical
request again also still attaches to the same analysis for an hour. Non-2xx responses carry `code`,
`message`, `remedy`, `retryable`, and optionally `installUrl` or `topUpUrl`.
See [Error codes](/reference/error-codes/).

The service's own limits apply: 300 checks per hour per token, and a body size
the service decides (`payload_too_large`).

## Ignore

`POST /v1/ignore` with `{"findingId": "<id>"}` answers
`{"findingId": "<id>", "status": "ignored"}`. Failures use the same closed
contract, including `finding_not_found`. 300 per hour per token.

## Unignore

`POST /v1/unignore` with `{"findingId": "<id>"}` answers
`{"findingId": "<id>", "status": "unignored"}`, also when the finding was not
ignored. The id may be the one the finding was ignored from or its id from any
later check of the repository. Failures are as for `/v1/ignore`.

## Ignored findings

`GET /v1/ignores?repoUrl=<url>&page=<n>` lists the findings the workspace
ignored in the repository `repoUrl` names (the URL a check sends), newest
first, 25 per page. `page` is optional, zero-based, at most 10000. It answers
`repository` (`fullName`), `total`, `page`, `pageSize` and `ignores`, each with
`id`, `policyId`, `findingId`, `title`, `path`, `startLine`, `severity`,
`ignoredAt` and `ignoredBy` (`username`, `displayName`, or null).
`findingId`, `title`, `path` and `startLine` are null when the finding is no
longer recorded. A repository the workspace does not have answers
`workspace_repository_not_found`.

## Account

| Route | Answer |
| --- | --- |
| `GET /v1/whoami` | `account`, `workspace` (`id`, `name`, `kind`, `role`, `current`), `plan` (`pro` or `free`), and `workspaces` |
| `POST /v1/workspace` with `{"workspaceId"}` | rebinds the login to that workspace |
| `GET /v1/repositories` | `repositories` (`id`, `provider`, `fullName`, `enabled`, `active`) with `activeRepositoryLimit`, `activeRepositories`, and `canManageRepositories` |
| `POST /v1/repositories/state` with `{"repositoryId", "enabled"}` | `{repositoryId, fullName, enabled, active}` |

Their failure codes add `account_required`, `workspace_not_found`,
`workspace_repository_not_found`, `owner_required`, and `browser_required` to
the check's.

## Device login

`POST /api/device` returns `deviceCode`, `userCode`, `verificationUri`,
`expiresIn`, and `interval`. `POST /api/token` with the device code returns
`202` while pending or `200` with a `token` once approved. The failure codes
are `slow_down`, `expired_token`, `access_denied`, and `device_flow_disabled`.
The approval page is `GET /device`, where a signed-in person enters the code.

## Versions

Every response names the service's newest CLI release in the
`SlopCop-Latest-Version` header. A client the service no longer accepts gets
`426` with code `client_outdated`.
