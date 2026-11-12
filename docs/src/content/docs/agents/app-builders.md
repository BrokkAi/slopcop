---
title: Lovable, Bolt, and MCP clients
description: "Connect the agent inside an app builder such as Lovable or Bolt to SlopCop's hosted MCP server, with an agent token, and the instructions that make it check every change."
sidebar:
  order: 5
---

The agents inside app builders cannot run the CLI. Lovable's agent has no
shell, and Bolt's runs in a WebContainer, which cannot start the native
`slopcop` binary. Both let you add a remote MCP server as a connector, so
SlopCop serves the [Model Context Protocol](https://modelcontextprotocol.io)
from the service itself:

```text
https://slopcop.com/v1/mcp
```

Its tools are one more client of the same hosted check: the same findings,
ignores, rate limits, and [credits](/account/credits/) as `slopcop check` and
`slopcop ignore`. Any MCP client that can call a remote server over streamable
HTTP with a bearer token can use it the same way.

<!-- TODO: Lovable and Bolt end to end against the hosted endpoint is
BrokkAi/bifrost-service#992, still open. Recheck the connector steps below
once it lands. -->

## Before you start

- **The project is on GitHub.** A check names the repository and the service
  applies the change in its own clone, so the app builder's project must be
  synced to a GitHub repository. Sending whole files instead of a diff, which
  an agent without git needs, works only for GitHub repositories.
- **A private repository needs the SlopCop GitHub App** on the account or
  organization that owns it, with the repository selected. See
  [Private repositories](/account/private-repositories/).
- **The workspace has the repository.** Add it once with `slopcop enable` from
  a checkout at a terminal; see
  [Repositories](/account/repositories/). The `check` tool can add it too, but
  only when the call sets `addRepository`, which the tool tells the agent to
  send only after you agree, since the workspace then pays for its checks.

## Make an agent token

The connector authenticates with a bearer token. Make a named **agent token**
on the workspace's settings page in the web app, for the workspace whose
credits the checks should use. The secret starts `slopcop_` and is shown once;
paste it straight into the connector.

An agent token acts only in the workspace it was made in, with its creator's
access there, and does not expire. The same page lists the workspace's tokens
with when each was last used, and revokes one: the next request with it is
refused. Removing a member from the workspace revokes their tokens. Treat a
token as a password.

The credential `slopcop login` stores also works as the bearer token, but a
token of its own for each app builder can be revoked without signing you out.

## Add the connector

<!-- TODO: menu labels come from the vendors' docs as of the MCP epic
(BrokkAi/bifrost-service#986); confirm them in each product. -->

**Lovable.** Under **Connectors**, add a **Custom MCP** server
([Lovable's guide](https://docs.lovable.dev/integrations/custom-mcp)). Set the
URL to `https://slopcop.com/v1/mcp` and choose bearer token (API key)
authentication with the agent token.

**Bolt.** Under **Connectors**, add a custom MCP server
([Bolt's guide](https://support.bolt.new/building/using-bolt/connect-mcp))
with the HTTP transport, the URL `https://slopcop.com/v1/mcp`, and bearer
authentication with the agent token. Turn it on for the project.

**Other MCP clients.** Point the client at the same URL with an
`Authorization: Bearer <token>` header.

Without a valid token every request answers HTTP `401`, so a connector's
connection test fails visibly instead of the tools failing later.

<!-- TODO: one-click MCP OAuth is BrokkAi/bifrost-service#991, not shipped.
Document the OAuth connection here once it is. -->

Connecting with OAuth instead of a pasted token is not available yet.

## The tools

| Tool | Takes | Does |
| --- | --- | --- |
| `check` | `repoUrl`, and the change as `patch` (a unified git diff) or `files`; optionally `baseCommit`, `failOn`, and `addRepository` | Checks the change, like `slopcop check`. |
| `check` | `checkId` (and `failOn`) | Waits for a check that is still running, without sending the change again. |
| `ignore` | `findingId` | Ignores a false positive, like `slopcop ignore`. |
| `unignore` | `findingId` | Undoes an ignore, like `slopcop unignore`. |
| `ignored` | `repoUrl`, optionally `page` | Lists the repository's ignored findings, like `slopcop ignored`. |

`files` is for an agent that cannot produce a diff, such as Lovable's: each
entry is `{path, content}` with a file's full new text, or
`{path, deleted: true}`. Files not listed are unchanged. Send exactly one of
`patch` or `files`.

An agent without git can leave `baseCommit` out. The service then checks
against the head of the repository's default branch and names that commit in
the answer. A change that does not apply there answers `base_out_of_sync`,
never a partial result; sync the project to GitHub and check again.

A finished check returns the same answer as the [HTTP API](/reference/api/),
with at most 100 new findings listed and `unlistedNewFindings` counting the
rest. A refusal, and a check that is still running (`check_running`), come back
as a tool error with the code and a remedy, so an agent never reads either as
a pass. [Error codes](/reference/error-codes/) lists the codes a check
shares with the CLI.

Each check through MCP is recorded with the harness `mcp`, and nothing else
about the session.

## Tell the agent to check every change

Add this to the instructions the app builder gives its agent for the project:

<!-- TODO: name the exact instruction settings in Lovable and Bolt once
BrokkAi/bifrost-service#992 has run there. -->

```text
This project is connected to SlopCop over MCP. After each change you make:
- Call the SlopCop `check` tool with the repository's GitHub URL as `repoUrl`
  and your change: as `patch` if you can produce a unified git diff, otherwise
  as `files` with each changed file's full new content. Leave `baseCommit` out
  unless you know the commit your change is based on.
- If it reports new findings, fix each one and call `check` again. If you are
  sure a finding is a false positive, call `ignore` with its id and tell the
  user why.
- If the answer is `check_running`, call `check` again with only the
  `checkId` it gave you. A running check is not a pass.
- If it is refused for any other reason, stop and tell the user the code and
  remedy. Never set `addRepository` unless the user has agreed to add the
  repository to the workspace.
```
