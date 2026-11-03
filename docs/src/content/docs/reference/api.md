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

`addRepository` is sent only after a person confirms adding the repository.
Run from an agent session, the request also carries an `attribution` object
with the harness, its version, the session id, and each model and effort with
the files it wrote. The service rejects any other field.

The response carries `status`, `baseCommit`, `applied`, `summary`, `findings`
(each with an `id` and `new`), `incomplete`, `inconclusive`, and `failure` when
the analysis did not complete. [Output formats](/checks/output-formats/) shows
the `json` rendering, which is this response plus `emptyChange` and
`skillUpdated` added by the CLI.

While the analysis runs, the service answers `503` with code `check_running`
and a `Retry-After`; the client asks again with the identical request, which
attaches to the same analysis for an hour. Non-2xx responses carry `code`,
`message`, `remedy`, `retryable`, and optionally `installUrl` or `topUpUrl`.
See [Error codes](/reference/error-codes/).

The service's own limits apply: 300 checks per hour per token, and a body size
the service decides (`payload_too_large`).

## Ignore

`POST /v1/ignore` with `{"findingId": "<id>"}` answers
`{"findingId": "<id>", "status": "ignored"}`. Failures use the same closed
contract, including `finding_not_found`. 300 per hour per token.

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
