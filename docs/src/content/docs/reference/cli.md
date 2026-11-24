---
title: CLI reference
description: "Every slopcop command and flag. Check, enable, status, login, logout, service, whoami, workspace, repo, ignore, unignore, ignored, dictionary, init, hook, and version."
sidebar:
  order: 1
---

```text
slopcop [flags]            # check the working tree (same as slopcop check)
slopcop [command] [flags]
```

Running `slopcop` with no subcommand runs the check. `slopcop --help` and
`slopcop <command> --help` print the same text as this page. `slopcop
completion <shell>` generates shell completion.

## Global flags

| Flag | Meaning |
| --- | --- |
| `--service URL` | the SlopCop check service URL for this command (also `SLOPCOP_SERVICE`); default `https://slopcop.com`. A command that uses another says so on stderr |
| `-v`, `--version` | print the version and exit |
| `-h`, `--help` | help for the command |

## `slopcop check`

Check the working tree with hosted static analysis and local review by
default. Use `--mode static` for the hosted half alone or `--mode ai` for
review without login, repository enablement, or supported origin. See
[Run a check](/checks/run-a-check/).

```console
slopcop
slopcop check --if-enabled
slopcop check --format json
slopcop --root ../repo --base origin/main
```

| Flag | Meaning |
| --- | --- |
| `--mode both\|static\|ai` | both halves (the default where reviewers are configured, outside CI), static only (the default otherwise), or review only |
| `--static`, `--ai` | shortcuts for the corresponding mode |
| `--intent TEXT` | user request if no session transcript is found |
| `--review-timeout DURATION` | review timeout, and the longest a check waits for another review or override in the same checkout (default `20m`) |
| `--root PATH` | repository path to check (default `.`) |
| `--base REV` | branch or revision the change started from; the base is its merge base with `HEAD` (default: merge base with the upstream branch, then with `origin/HEAD`, then `HEAD`) |
| `--base-exact REV` | exact base commit or revision, used without taking a merge base |
| `--include PATH_OR_GLOB` | opt matching files back into upload and local review when the default 500,000-byte JSON/binary filter would omit them; repository-relative, repeatable, quote globs |
| `--format human\|json\|sarif` | report format (default `human`) |
| `--sarif-file PATH` | also write the report as SARIF to this file, so one check gives `json` on stdout and SARIF for a code scanning upload |
| `--fail-on never\|finding\|note\|warning\|error` | exit `1` when a new static finding is at or above this level (default `finding`), or when a reviewer reported any finding (every level but `never`) |
| `--certainty definite\|all` | report and fail on only `definite` findings (default), or on `possible` and `heuristic` ones too |
| `--policy PATH` | repository-relative `.rqlp` policy file to evaluate (repeatable) |
| `--no-policy-discovery` | do not include `.slopcop/policies` rules automatically |
| `--policy-id ID`, `--policy-pack PACK`, `--policy-category CATEGORY` | run only these built-ins instead of the default catalog (each repeatable); repository rules still run |
| `--timeout DURATION` | how long to wait for the result; a check still running then exits `3` (default `10m0s`) |
| `-w`, `--workspace NAME\|ID` | workspace to run in, and bill, by name or id, instead of the one this checkout is mapped to or this login is bound to (also `SLOPCOP_WORKSPACE`) |
| `--if-enabled` | skip a repository the workspace has disabled or has past its active limit, exiting `0` without sending a check; one it has never added is added |
| `--verbose` | print the resolved request to stderr, and name each inconclusive check in `human` |

Exit codes: `0` clean, `1` new findings, `2` incomplete or failed, `3` still
running, `4` refused and a person must act, `130`/`143` interrupted. See
[Exit codes](/checks/exit-codes/).

## `slopcop enable`

Optional. Add the repository the `origin` remote names to the workspace that
checks and bills it, run its first check, and map this machine's checkouts of
it to that workspace. It does not ask: it says which workspace it added the
repository to, and how to move it (`slopcop workspace map <name>`) or
remove it (`slopcop repo disable`). It exits with the first check's result. A
check adds a repository the workspace has never added by itself, so `enable`
is for adding one up front, picking its workspace, or turning a disabled
repository back on, which it does only at a terminal. See
[Repositories](/account/repositories/).

| Flag | Meaning |
| --- | --- |
| `--root PATH` | repository path (default `.`) |
| `-w`, `--workspace NAME\|ID` | workspace to add it to, by name or id, instead of the one this checkout is mapped to or this login is bound to (also `SLOPCOP_WORKSPACE`) |

## `slopcop status`

Show the account and workspace that bill checks, and whether the repository the
`origin` remote names is added to that workspace and active. It is the quick
way to answer whether a checkout is handled by SlopCop. See
[Repositories](/account/repositories/).

| Flag | Meaning |
| --- | --- |
| `--root PATH` | repository path (default `.`) |
| `--format human\|json` | output format (default `human`) |
| `-w`, `--workspace NAME\|ID` | workspace to inspect, by name or id, instead of the one this checkout is mapped to or this login is bound to (also `SLOPCOP_WORKSPACE`) |

States: `active`; `disabled`; `enabled, over the limit`; `not added` (the
next check adds it); and `unsupported` (SlopCop checks
repositories on github.com, gitlab.com and bitbucket.org). `json` carries the
same state as `active`,
`disabled`, `over_limit`, `not_added`, or `unsupported`, with the repository's
`url`, `fullName`, `provider`, `id`, `enabled`, and `active`. Exits `2` when
the checkout has no `origin`, or the service cannot answer.

`Plan` is the plan of the workspace shown. The service tells a login the plan
of the workspace it is bound to and no other, so when the workspace shown is
another one (chosen by `--workspace`, `SLOPCOP_WORKSPACE` or a mapping) there
is no `Plan` line and `json` has no `plan`. A `Login` line names the login's
own workspace and its plan instead, and `json` carries them as `login`
(`workspace` and `plan`). `json` no longer has `planOf`.

When other workspaces of the account have the repository, an `Also in` line
names each with its state there, and `json` carries them as `otherWorkspaces`
(`id`, `name`, `kind`, `role`, and `state`, which adds `unknown` for a
workspace the service would not answer for). For a GitHub repository a
`PR checks` line follows: pull request checks are turned on per workspace in
the web app and do not follow the workspace `status` prints. See
[Pull request checks have their own workspace](/account/workspaces/#pull-request-checks-have-their-own-workspace).

## `slopcop login`

Sign in to the SlopCop check service. With no flags, starts the browser device
flow: prints a link that opens the approval page with the code filled in, then
waits for approval. Login does not install the agent skill. See
[Sign in](/account/sign-in/).

| Flag | Meaning |
| --- | --- |
| `--token TOKEN` | store an existing token instead of starting the browser flow |
| `--default` | also make this service the default for every command. It asks at a terminal and refuses when stdin is not one |

Each service keeps its own login. With `--service`, login signs in to that
service, leaves the other logins as they are, and does not change the default
unless `--default` is passed. The one exception is a default with no login,
as on a machine's first login: then the service you sign in to becomes the
default and login says so, so an install command from a staging or local
deployment signs in the commands that follow it.

## `slopcop logout`

Forget the credential stored for one service: the one `--service` or
`SLOPCOP_SERVICE` names, else the default. Other logins stay.

| Flag | Meaning |
| --- | --- |
| `--all` | forget the credential for every service |

## `slopcop service`

List the services this machine is signed in to, or change the default.

| Command | Meaning |
| --- | --- |
| `slopcop service list` | the default service and every service with a stored login; `--format json` has `services`, each with `service`, `default`, `signedIn` |
| `slopcop service use URL` | make a service the default for every command without `--service`. It asks at a terminal and refuses when stdin is not one |

## `slopcop whoami`

Show the account behind this login, the workspace it is bound to (the one its
checks and ignores run in, and bill, unless the checkout is mapped to
another), that workspace's plan, and the service URL. In a checkout this
machine maps to a workspace, a `Checkout` line names that workspace. With logins stored for more than one service, it also lists them and
marks the default. Run it before a check when it matters which workspace pays.

| Flag | Meaning |
| --- | --- |
| `--format human\|json` | output format (default `human`); `json` has `service`, `account`, `workspace`, `plan`, `services`, and `checkout` in a mapped checkout |

## `slopcop workspace`

List your account's workspaces, map a repository to one of them, or bind this
login to another of them. See
[Workspaces](/account/workspaces/).

| Command | Meaning |
| --- | --- |
| `workspace list [--format human\|json]` | the account's workspaces; `*` marks this login's |
| `workspace use <name\|id> [--format human\|json]` | bind this login to another workspace; moves every agent and script using the login, so it asks `[y/N]` and refuses when stdin is not a terminal |
| `workspace map <name\|id> [--root PATH]` | check this checkout's repository in that workspace from now on, on this machine; it decides which workspace pays, so it asks `[y/N]` and refuses when stdin is not a terminal. Pull request checks are unaffected, and it says so |
| `workspace unmap [--root PATH]` | forget the mapping, so the repository checks in the login's workspace again; asks `[y/N]` at a terminal |
| `workspace mappings [--format human\|json]` | the repositories this machine maps, and to which workspace |

## `slopcop repo`

List the repositories of this login's workspace, or of the one `--workspace`
names, or enable or disable one. See [Repositories](/account/repositories/).

| Command | Meaning |
| --- | --- |
| `repo list [--format human\|json]` | the workspace's repositories and whether each is active, disabled, or enabled over the limit |
| `repo enable <owner/name\|id>` | enable a repository, within the workspace's active limit (owner only; asks you to type the name) |
| `repo disable <owner/name\|id>` | disable a repository: it is not checked (owner only; asks you to type the name) |

All take `-w`/`--workspace`. `enable` and `disable` take `--format` for their
answer, and refuse when stdin is not a terminal.

## `slopcop ignore <finding-id>`

Stop reporting one static finding that is a false positive. The id is the UUID
a check printed. A local review finding's `rv-` id is refused with the
`slopcop override` command to run instead. Ignoring covers that one finding in its repository for your workspace,
not its rule. Takes `-w`/`--workspace`. Exits `0` with a confirmation, `2` with
the service's message otherwise. See [False positives](/checks/false-positives/).

## `slopcop unignore <finding-id>`

Undo an ignore: later checks of the repository report the finding again. The id
is the one it was ignored from, or the same finding's id from any later check.
Unignoring a finding that is not ignored is fine. Takes `-w`/`--workspace`, and
exits like `ignore`. See [False positives](/checks/false-positives/#undo-an-ignore).

## `slopcop ignored`

List the findings your workspace ignored in this repository (the one `origin`
names), newest first, 25 per page: each one's finding id for `unignore`,
severity, location, rule, title, and when and by whom. A finding the service no
longer records has no id. Flags: `--page <n>` (from 1), `--format human|json`,
`--root <path>`, `-w`/`--workspace`. Exits `0`, or `2` with the service's
message.

## `slopcop dictionary`

Manage your workspace dictionary: the identifier words that checks in the
workspace accept, so misspelled-identifier findings stop reporting them.

| Subcommand | What it does |
| --- | --- |
| `dictionary add <finding-id>` | add a misspelled-identifier finding's suggested word |
| `dictionary add --word <word>` | add a word: one identifier part of ASCII letters, such as `parsr`, `HTTP` or `Kubectl` |
| `dictionary list [--format human\|json]` | the words, with when, by whom, and from which finding each was added |
| `dictionary remove <word>` | remove a word (owners and managers only) |

All take `-w`/`--workspace`. Each exits `0`, or `2` with the service's message.
See [False positives](/checks/false-positives/#add-a-word-to-the-dictionary).

## `slopcop init`

Run once to install user-level skills and static-only Stop hooks and push gates
for Claude Code, Codex and Grok. The gate enforces only repositories where
SlopCop has review state, so it does not block unrelated projects. When run in
a checkout, init also removes the hooks, skills and exclude entries written by
the earlier per-repository releases; other checkouts migrate themselves the
first time their old hook runs the upgraded binary. See
[The agent skill](/agents/skill/) and [The Stop hook](/agents/stop-hook/).

| Flag | Meaning |
| --- | --- |
| `--no-skill` | do not install or update the agent skill |
| `--no-hooks` | do not install or update the automatic check hook |
| `--remove-hooks` | remove user-level and legacy repository SlopCop hooks and nothing else |

## `slopcop doctor`

Report whether SlopCop's skill, Stop hook and push gate are installed for each
agent harness here (Claude Code, Codex, Grok), and the init command that adds
what is missing. Run in a checkout, it also lists hooks and skills left by the
earlier per-repository releases. Local only: it sends nothing, probes no
reviewer and changes nothing. Flags: `--root <path>`, `--format human|json`.
Exits `0`. See [Checking your setup](/agents/setup-check/).

## `slopcop hook <claude|codex|grok|kimi>`

Run a check from a Claude Code, Codex, Grok or Kimi Code Stop hook. It reads
the hook's input on stdin and answers in the harness's hook protocol.
`slopcop init` installs it as `slopcop hook claude || true`,
`slopcop hook codex || true` and `slopcop hook grok || true`; Kimi Code's is
added by hand (see [Grok and Kimi Code](/agents/grok-kimi/)). It is not meant
to be run by hand.

## `slopcop config` and `slopcop override`

`slopcop config get|set|unset <key> [value]` manages `mode` (`both`,
`static`, `ai`), `review.reviewers` (1–4 comma-separated entries, in order;
duplicates allowed), `review.fallbacks` (the same form; reviewers that stand in
for one that fails), `review.timeout` (default `20m`), and
`review.minCompleted` (default 2, at most the number of reviewers: how many
must finish for a clean review), `review.provider` (`copilot` runs
`terra`, `sol`, `haiku`, `sonnet` and `opus` through GitHub Copilot CLI), and
`review.models.<ollama|openrouter>` (the model an opt-in family that takes one
reviews on: an installed model for `ollama`, a `vendor/model` for `openrouter`). Adapter command
overrides are under `review.adapters.<codex|dsh|claude|copilot|antigravity|kimi|grok|opencode|openrouter>.command`.
The opt-in `gemini`, `kimi`, `grok` and `ollama` families are used only when you name them; see
[Gemini through Antigravity](/reference/configuration/#gemini-through-antigravity-opt-in),
[Kimi through Kimi Code](/reference/configuration/#kimi-through-kimi-code-opt-in),
[Grok through Grok Build](/reference/configuration/#grok-through-grok-build-opt-in)
and [Ollama through OpenCode](/reference/configuration/#ollama-through-opencode-opt-in).
The opt-in `openrouter` family reviews with the model in
`review.models.openrouter`, and `ollama` with the one in `review.models.ollama`; see
[OpenRouter](/reference/configuration/#openrouter-opt-in).

`slopcop override <rv-id> --reason "..."` overrides one local review finding
that is wrong, by the `rv-` id in `review.findings`, for every later review in
the clone, on any branch or worktree. Run `check` again afterwards: the same
review is reused and leaves the finding out. Later reviews are shown it with
the reason and told not to raise it again, and a finding that repeats it, even
reworded near the same lines, is listed under `review.overridden` and does not
fail the check. `--undo <rv-id>` removes one (the overridden id, or a
left-out finding's id) and withdraws the push approval of every tree that
relied on it; `--list [--format json]` lists them with their reasons. See
[Override a review finding](/checks/false-positives/#override-a-review-finding).

`slopcop override --reason "..."` accepts demonstrably incorrect findings
from the last complete review of the same working-tree snapshot, provided
the static half passed or did not run. It records the reason and cannot
accept failed or incomplete reviews or real bugs. It covers that snapshot
only. Both forms are recorded locally, never on the service, so they accept
`-w`/`--workspace` from the `check` they follow and ignore it.

Each review is shown the findings of every earlier round since the last
clean review, with whether the lines a finding cites have changed since, and
the reason of an override beside the findings it accepted. Reviewers are told
not to raise an answered point again unless the code shows the reason is
wrong, so write the reason for them: say why the finding does not hold.

The PreToolUse hook runs `slopcop gate --hook claude` in Claude Code (and
Grok) and `slopcop gate --hook codex` in Codex. It blocks `git push`, `gh pr create`, and the GitHub MCP tools that push files or
open a pull request until HEAD's tree has a passing check or valid override. It also
blocks any shell command that names `refs/slopcop/` or `.git/slopcop`.

## `slopcop review show`

Print the latest local review of this checkout again, in `--format human`,
`json` or `sarif`, without running a reviewer or spending a check. Each
reviewer's reply is rebuilt from its log under `<git-dir>/slopcop/` and its
findings are parsed again, so the reply is shown in full even when the
report the check printed was not. It only shows the review: it approves
nothing and changes no state.

When `slopcop override` accepted the whole review, the output says so with the
recorded reason, and whether the override still applies to the working tree as
it is now. The findings stay listed and `status` stays `findings`. In JSON,
`approved` stays `false`, because it says whether this run recorded push
approval, and `reviewOverride` carries the override:

| `reviewOverride.status` | Meaning |
| --- | --- |
| `accepted` | The override applies to the working tree as it is now, and the tree has its push approval. |
| `tree_changed` | The working tree is no longer the accepted `tree`; `currentTree` names it. Run `slopcop check`. |
| `withdrawn` | The tree's push approval is gone, or a finding override the review relied on was undone. Run `slopcop check`. |
| `unverified` | The override could not be compared with the repository, so it is not claimed to apply. |

`reviewOverride` also has `reason`, `tree` and `time`, and is absent when the
review has no override. SARIF carries it as the `slopcop.reviewOverride` run
property. A later check of the tree records a new decision, so its review
starts with no override.

## `slopcop comment`

Comment a check's findings on the GitHub pull request it checked. It reads the
report `slopcop check --format json` wrote, so the check runs once. It adds one
pull request review with an inline comment on each new finding whose lines the
pull request changes, static and review findings alike, and one summary
comment that says what the check found and lists the findings no line of the
diff can hold. A later run updates the summary in place and does not comment a
finding it already commented. A clean check writes no summary unless an
earlier run left one, which it updates.

```console
slopcop check --base origin/main --format json > slopcop.json
slopcop comment --report slopcop.json
```

| Flag | Meaning |
| --- | --- |
| `--report PATH` | the JSON report, or `-` for stdin (required) |
| `--repo OWNER/NAME` | repository (default `$GITHUB_REPOSITORY`) |
| `--pr NUMBER` | pull request (default: the workflow event's) |
| `--commit SHA` | head commit the check ran on (default: the event's pull request head) |
| `--api-url URL` | REST API root (default `$GITHUB_API_URL`, else `https://api.github.com`) |
| `--dry-run` | print the summary and write nothing to GitHub |

The token comes from `GITHUB_TOKEN` (or `GH_TOKEN`) and needs
`pull-requests: write`. With a read-only token, as on a pull request from a
fork, nothing is written to the pull request. In GitHub Actions the summary
also goes to the job summary, and the step's outputs carry `status`,
`exit-reason`, `static-findings`, `review-findings`, `headline` and
`summary-url`. It exits `0` whatever the check found: the check's exit code is
the gate. The [GitHub Action](/checks/ci/) runs it for you.

## `slopcop update`

Update slopcop to the latest release. A slopcop installed by the install script
replaces its own binary, after checking the download against the release's
checksums; one installed through npm, pip, pipx or uv is told the command to
run. See [Automatic updates](/get-started/install/#automatic-updates).

## `slopcop version`

Print the version. A release binary prints its tag; a plain `go install` prints
`dev`.

## Environment variables

| Variable | Meaning |
| --- | --- |
| `SLOPCOP_TOKEN` | a token for this run; wins over the stored credential |
| `SLOPCOP_SERVICE` | the service URL; `--service` wins over it |
| `SLOPCOP_WORKSPACE` | the workspace for `check`, `ignore`, `unignore`, `ignored`, `dictionary`, and `repo`; `-w` wins over it, and it wins over a checkout's mapping |
| `SLOPCOP_NO_UPDATE_NOTICE` | any non-empty value silences the upgrade notice and turns automatic updates off |
| `SLOPCOP_NO_AUTO_UPDATE` | any non-empty value keeps slopcop from replacing its own binary; the notice still prints |
| `CLAUDE_CODE_SESSION_ID`, `CLAUDE_CONFIG_DIR` | set by Claude Code; name the session the CLI reads model attribution from |
| `CODEX_THREAD_ID`, `CODEX_HOME` | set by Codex; the same |
| `OPENCODE_SESSION_ID`, `OPENCODE_DB`, `XDG_DATA_HOME` | set by OpenCode or you; the same |
| `GROK_SESSION_ID`, `GROK_HOME` | set by Grok; the same |
| `KIMI_CODE_HOME` | Kimi Code's data directory, read by its Stop hook |

See [Configuration](/reference/configuration/) for precedence and the files
the CLI writes.
