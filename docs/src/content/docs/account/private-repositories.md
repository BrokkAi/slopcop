---
title: Private repositories
description: "A private repository needs a connection the workspace owns: the SlopCop GitHub App, or a GitLab.com or Bitbucket Cloud connection. What the service can read, and the codes a missing connection answers."
sidebar:
  order: 4
---

A check sends the repository's URL and a patch; the service applies the patch
to the base commit in its own clone. For a public repository on GitHub,
GitLab.com, or Bitbucket Cloud that clone needs nothing from you. For a private
one the service must be able to read the repository, and your account must be
allowed to.

## GitHub

Install the [SlopCop GitHub App](https://github.com/apps/slop-cop) on the
account or organization that owns the repository, and select the repositories
it may read. The App asks for read-only access to contents and metadata; it
never writes to your repository. Optionally it can be granted issues access so
the web app can file findings as issues.

The workspace connects to the App from its settings in the web app. Each member
who checks a private repository also authorizes SlopCop with their own GitHub
account once, so the service can confirm they may read it: a manager or owner
role in the workspace does not substitute for the member's own repository
rights.

| Code | Meaning |
| --- | --- |
| `app_not_installed` | The App is not installed on the repository's owner. Install it at the printed link. |
| `app_no_repo_access` | The App is installed but this repository is not selected. Add it in the App's settings on GitHub. |
| `no_access` | Your account cannot read the repository, or has not authorized SlopCop. |

## GitLab.com and Bitbucket Cloud

A workspace connects a GitLab.com or Bitbucket Cloud account from its settings
in the web app, and the connection's read access decides which private
repositories its checks may use. Subgroup namespaces on GitLab are supported.
Self-hosted GitLab and Bitbucket Server are not supported.

`gitlab_not_connected` means the workspace has no GitLab connection, or it
needs to be reconnected. Enabling or disabling a private GitLab or Bitbucket
repository answers `browser_required`: only the web app can change it.

## When a connection needs attention

When a connection's token expires or its permissions change, the web app's
Repositories page names the connection with a link to the workspace's settings,
since checks of that provider's private repositories can be refused until it is
reconnected.

## What the service reads

Read-only access to source. The service never builds or runs your code and
never sends it to a language model. A finding carries the rule, severity,
certainty, file, and line, with no source excerpt. See
[What leaves your machine](/account/privacy/).
