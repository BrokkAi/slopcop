# SlopCop

**Hosted static analysis and local review for every change.**

[![CI](https://github.com/BrokkAi/slopcop/actions/workflows/ci.yml/badge.svg)](https://github.com/BrokkAi/slopcop/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/BrokkAi/slopcop)](https://github.com/BrokkAi/slopcop/releases/latest)
[![npm](https://img.shields.io/npm/v/@brokkai/slopcop)](https://www.npmjs.com/package/@brokkai/slopcop)
[![PyPI](https://img.shields.io/pypi/v/brokk-slopcop)](https://pypi.org/project/brokk-slopcop/)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25%2B-blue.svg)](https://go.dev/)

In its default `both` mode, `slopcop` sends one git patch, from the base commit
to the working tree, to the hosted check API at <https://slopcop.com>. The
service applies it, runs the hosted policy catalog plus the repository rules you
selected, and returns the report: every finding with its severity, the file and
line that triggered it, and the certainty behind it. The analysis is
[Bifrost](https://github.com/BrokkAi/bifrost), an open-source static analysis
engine, so a static finding traces back to a rule. Local review findings are
model judgments that need inspection.

The full documentation is at <https://docs.slopcop.com>.

The hosted static analyzer needs no per-language install. Local review uses your
existing harness logins and Node.js with `npx`. `--mode ai` needs no SlopCop
login, enabled repository, or supported origin. Use `--mode static` for a
hosted-only check or CI without reviewer harness logins. A check never adds a
repository; `--if-enabled` skips one the workspace has not enabled, and says it
was skipped.

## Install

npm, PyPI, the release archive, and the shell and PowerShell installers all
install the same release binary for macOS, Linux, and Windows on x64 and
arm64, so `slopcop version` reports which release it came from. The Go
toolchain can build that binary from source instead.

| Channel | Platforms | Needs |
| --- | --- | --- |
| [npm](#npm) | macOS, Linux, Windows | Node 22 or newer |
| [PyPI](#pypi) | macOS, Linux, Windows | Python 3.9 or newer |
| [Release archive](#release-archive) | macOS, Linux, Windows | nothing |
| [Shell installer](#shell-installer) | macOS, Linux | curl |
| [PowerShell installer](#powershell-installer) | Windows | PowerShell |
| [Go](#go) | built from source | Go 1.25 or newer |

### npm

```console
npm install --global @brokkai/slopcop
slopcop --help
```

The tarball carries the release binary for every supported platform, so it
needs no Go toolchain. `npx @brokkai/slopcop` runs it without installing it.

### PyPI

```console
pip install brokk-slopcop
```

`pipx install brokk-slopcop` or `uv tool install brokk-slopcop` puts it in an
environment of its own. The wheels carry the release binary, so no Go toolchain
is needed either. The package is `brokk-slopcop` because PyPI already has an
unrelated project named `slopcop`; the command it installs is still `slopcop`,
and `python -m brokk_slopcop` runs the same binary.

### Release archive

Each release publishes `slopcop_v<version>_<os>_<arch>.tar.gz` (`.zip` on
Windows) holding the binary, `README.md`, and `LICENSE`, plus a `checksums.txt`
covering every archive. Download them from
<https://github.com/BrokkAi/slopcop/releases/latest> and check the sha256
before unpacking:

```console
ver=0.0.3; os=linux; arch=amd64   # darwin/arm64 on Apple silicon
base=https://github.com/BrokkAi/slopcop/releases/download/v$ver
curl -fsSLO "$base/slopcop_v${ver}_${os}_${arch}.tar.gz"
curl -fsSLO "$base/checksums.txt"
grep "${os}_${arch}" checksums.txt | sha256sum -c -   # shasum -a 256 -c - on macOS
tar -xzf "slopcop_v${ver}_${os}_${arch}.tar.gz" slopcop
sudo install -m 755 slopcop /usr/local/bin/slopcop
```

### Shell installer

```console
curl -fsSL https://raw.githubusercontent.com/BrokkAi/slopcop/master/install.sh | sh
```

It takes the latest release, verifies the archive against `checksums.txt`,
installs `slopcop` to `$HOME/.local/bin`, and adds that directory to the
shell profile. `SLOPCOP_VERSION`, `SLOPCOP_INSTALL_DIR`, and
`SLOPCOP_NO_MODIFY_PATH` override its defaults.

### PowerShell installer

```powershell
irm https://raw.githubusercontent.com/BrokkAi/slopcop/master/install.ps1 | iex
```

It takes the latest release, verifies the archive against `checksums.txt`,
installs `slopcop.exe` to `%LOCALAPPDATA%\Programs\slopcop`, and adds that
directory to the user `Path`. `SLOPCOP_VERSION`, `SLOPCOP_INSTALL_DIR`, and
`SLOPCOP_NO_MODIFY_PATH` override its defaults.

### Upgrade notice

Every response from the service names its newest release in the
`SlopCop-Latest-Version` header. When that is newer than the running build,
the CLI prints one line on stderr, never stdout, so `--format json` and
`sarif` stay clean:

```text
slopcop v0.0.9 is available (you have v0.0.7). Upgrade: npm install --global @brokkai/slopcop@latest
```

The command follows the channel the binary was installed through (npm, npx,
pip, pipx, or uv), judged by where it lives, and links back here otherwise.
The notice prints at most once a day; the time of the last one is kept in
`slopcop/update-notice` next to the config. `dev` builds, `Stop` hook runs, and
runs with `$SLOPCOP_NO_UPDATE_NOTICE` set to any non-empty value never print
it. A service that no longer accepts a version answers `client_outdated`
(HTTP 426), which exits `2`.

### Go

```console
go install github.com/BrokkAi/slopcop/cmd/slopcop@latest
```

Go 1.25 or newer is required to build. A plain `go install` reports `dev` as
its version; in a clone, `make install` stamps the version from `git describe`.

## Set it up in an agent

The binary carries the agent skill that teaches a coding agent how to run
slopcop, plus an automatic check hook for Claude Code and Codex. Run
`slopcop init` inside the repository. It installs the skill into the
repository-local directories below and names what it did:

| Location | Harness |
| --- | --- |
| `.agents/skills/slopcop/` | Codex and other agents |
| `.claude/skills/slopcop/` | Claude Code and other agents |

Init adds these paths to `.git/info/exclude`, leaves tracked files alone, and
removes SlopCop's old user-level skill copies and hooks.

A skill that is already there is left alone by init, so running it again is
safe. A check updates copies already in the repository, but creates none; login
does not install the skill. An existing copy is rewritten when it differs from
the skill bundled into the binary, so an agent follows the instructions of the
slopcop version it runs. Each install records the version that wrote it, and
what each file held, in `.slopcop-skill.json` in the skill folder. An update
names the version it replaces, the new one and the files that changed, for
example:

```
slopcop: updated the slopcop skill at .claude/skills/slopcop from v0.0.9 to v0.0.10 (changed: SKILL.md). Reread it if you loaded the old one.
```

It removes the files the new skill no longer has. A skill with local edits,
one that matches neither its record nor any release, is copied first to
`skill-backups/<harness>-<time>` next to the slopcop configuration, outside
every skills directory so no harness loads it as a second skill, and the
notice says where. A check's `json` and `sarif` reports carry the same update
(see below). `slopcop init --no-skill` skips the install and any update of
it.

Init also adds a static-only `Stop` hook for Claude Code and Codex, and
leaves everything else in the file as it was:

| File | Harness |
| --- | --- |
| `.claude/settings.local.json` in the repository | Claude Code |
| `.codex/hooks.json` in the repository | Codex |

The Claude Code and Codex settings also get a PreToolUse push gate. It
blocks `git push`, `gh pr create`, and the GitHub MCP tools that push files or
open a pull request until HEAD's tree has a passing check or valid override.
Grok runs the Claude Code gate too, in a folder it trusts. Codex runs its gate
only once you trust it with `/hooks`.

Codex reads the repository's `.codex/hooks.json` only once you trust the
project in Codex, and runs a new or changed hook only once you trust it too:
open Codex in the repository and review it with `/hooks`. Init never writes that trust itself, and reminds you each
time it runs, since Codex says nothing of a hook it skips. The hook stays the
same from one release to the next, so an update does not ask for it again.

When the agent stops, the hook checks the working tree through the same
hosted API, but only when all of these hold:

- the session wrote files in the repository with its edit tools (a Codex
  session's subagents included), or the working tree has a changed
  dependency manifest or lockfile (see below);
- slopcop is signed in, and the workspace a check would use
  (`$SLOPCOP_WORKSPACE`, or the login's) has the repository active
  (`slopcop repo list`), so nothing is sent from other repositories;
- the tree differs from the last check in the session, including a check the
  agent ran by hand.

An agent often adds a dependency through its shell, with `npm install`,
`pnpm add`, `cargo add` or `uv add`, which no edit tool records. So the hook
also checks when git shows one of these files changed, added or untracked in
any directory of the repository: `package.json`, `package-lock.json`,
`npm-shrinkwrap.json`, `pnpm-lock.yaml`, `yarn.lock`, `Cargo.toml`,
`Cargo.lock`, `pyproject.toml`, `uv.lock`, `poetry.lock`, `requirements*.txt`,
`go.mod` and `go.sum`. Files git ignores and those under `node_modules` do
not count. The hook recognises these files by name only: it never reads
them, runs a package manager or asks an advisory database; the hosted check
reads them like any other file in the change. A changed dependency file
counts as one the session wrote, since the hook cannot tell which command
changed it, so its new findings are handed to the agent like those in files
it edited, even if the file was already changed when the session began.

New findings in files the session wrote are handed to the agent to fix, once
for a given tree and at most twice in a row; then the agent is let go and you
are told what remains. A finding ignored with `slopcop ignore` is not handed
back. New findings in other files on the branch are only mentioned. The hook
never keeps the agent going on its own failures: a refusal the service calls
final, such as an expired login, is reported once and the hook stays quiet for
the rest of the session, until a check by hand completes; a brief outage is
only reported. The hook runs as `slopcop hook claude || true` (and
`slopcop hook codex || true`, or `|| exit /b 0` under Windows' `cmd`), so even
a `slopcop` that cannot run it, such as an older one first on `PATH`, lets the
agent stop; init warns when that is the case, because no check would run.

Use `slopcop init --no-hooks` to install only the skill, or
`slopcop init --remove-hooks` to remove SlopCop's hooks and nothing else.
Ordinary checks do not reinstall a removed hook. The skill remains the way to
check from other harnesses.

The hook is dormant in a repository until it is enabled in the workspace. Run
`slopcop enable` in the checkout, at a terminal, to add it and run its first
check. The hook then checks what an agent writes and hands findings back; it
never adds a repository and sends nothing for one that is not active.

### Managed rollout

An administrator can deploy the `Stop` hook through managed configuration
instead of having each user run `slopcop init`. Device management must still
put `slopcop` on `PATH`, and each user still signs in.

- **Claude Code:** add the handler `slopcop init` writes to the `hooks` key of
  the managed settings (`managed-settings.json` or a file in
  `managed-settings.d/`): a `Stop` group with `"type": "command"`,
  `"command": "slopcop hook claude || true"`, `"timeout": 720` and
  `"statusMessage": "Checking the change with SlopCop"`. Under
  `allowManagedHooksOnly` the hooks init writes to the repository's
  `.claude/settings.local.json`, the push gate included, are blocked, so the
  managed hook is the only one that runs. Claude Code runs
  an identical handler from several settings files once.
- **Codex:** define the `Stop` hook under `[hooks]` in the managed
  `requirements.toml` (`command = "slopcop hook codex || true"`,
  `command_windows = "slopcop hook codex || exit /b 0"`, `timeout = 720`).
  Managed hooks are trusted by policy, so users skip `/hooks`. Codex installs
  nothing from `managed_dir`.

To keep the push gate, deploy its `PreToolUse` handler the same way
(`"command": "slopcop gate --hook claude"`, or `slopcop gate --hook codex`,
with no `|| true`); the Managed rollout page has both.

Users then run `slopcop init --no-hooks` to install only the skill. See
[Managed rollout](https://docs.slopcop.com/agents/managed-rollout/).

## Sign in

```console
slopcop login                           # browser device flow
slopcop login --token "$SLOPCOP_TOKEN"  # existing credential, for CI and agents
```

The browser flow prints a link that opens the approval page with the code
filled in, plus the page and code to type in instead, and stores the token once
the login is approved. Approve only if the page shows the code your terminal
printed. The service URL defaults to `https://slopcop.com`; `--service`,
`$SLOPCOP_SERVICE`, or a stored value override it.

The credential lives in `slopcop/config.json` under the user config directory
and is written owner-only. `slopcop logout` clears it.

Precedence:

| Value | Order |
| --- | --- |
| Service URL | `--service`, `$SLOPCOP_SERVICE`, stored by `slopcop login`, `https://slopcop.com` |
| Token | `$SLOPCOP_TOKEN`, stored value |
| Workspace (`check`, `enable`, `status`, `ignore`, `unignore`, `ignored`, `repo`) | `-w`/`--workspace`, `$SLOPCOP_WORKSPACE`, the one the login is bound to |

## Workspace and repositories

A login runs its checks and ignores in, and bills, one workspace: the one
active in the browser when the login was approved.

```console
slopcop whoami                     # account, this login's workspace, its plan, the service URL
slopcop status                     # this checkout's repository, and whether the workspace checks it
slopcop enable                     # add this checkout's repository to the workspace, at a terminal
slopcop workspace list             # the account's workspaces; * marks this login's
slopcop workspace use "Acme team"  # move this login (not the browser) to another, by name or id
slopcop check -w "Acme team"       # check in, and bill, another workspace this once
slopcop repo list                  # the workspace's repositories: active, disabled, or over the limit
slopcop repo disable acme/widgets  # stop checking and charging for one (owner only)
slopcop repo enable acme/widgets   # turn it back on, within the workspace's active limit
```

`status`, `whoami`, `workspace list` and `repo list` take `--format human|json`,
as do `workspace use` and `repo enable|disable` for their answer. Every active
repository after a workspace's first costs credits every 30 days; a disabled
one is not checked or charged and keeps its reports, findings and settings.
`repo enable` and `repo disable` ask you to type the repository's `owner/name`
and refuse when stdin is not a terminal: an agent or CI job runs with your
login, and the service cannot tell it from you, so changing what the workspace
pays stays a step you take yourself.
`workspace use` moves every agent and script using this login, not only the
one that runs it, so it asks `[y/N]` and refuses when stdin is not a terminal.
To check in another workspace without moving anyone, `check`, `status`,
`ignore`, `unignore`, `ignored` and `repo` take `-w`/`--workspace <name|id>`
(or `$SLOPCOP_WORKSPACE`) for that one command; the login stays where it was. Give agents that share a login their
own `--workspace` instead of switching it.

Names match without regard to case; when two workspaces share a name, or two
providers a repository name, pass the id the listing prints.

`slopcop enable`, run inside a checkout at a terminal, adds the repository the
`origin` remote names to the workspace that checks and bills it, and runs its
first check. It names the workspace and what adding the repository costs, asks
you to type the repository's `owner/name`, and refuses when stdin is not a
terminal. A repository that is already added but disabled is enabled again;
one that is already active is left alone. An agent runs with your login and
the service cannot tell it from you, so adding a repository stays a step you
take yourself.

The static half never adds a repository. A check of one the workspace has not
added exits `4` with `repository_not_enabled` and sends nothing; `status` shows
the same state, and a check that is not at a terminal asks nothing. Once a
person has approved adding one, `check --add-repository --workspace <name|id>`
carries that approval without a terminal; the flag needs `--workspace` (the flag
itself, not `$SLOPCOP_WORKSPACE`), so an approval given for one workspace cannot
add the repository to the one the login happens to point at. The flag changes
nothing for a repository already in that workspace. `slopcop enable` says `added
owner/name to "Workspace"` on stderr, and `json` reports `"repositoryAdded":
true`. Deleting repositories, archiving workspaces, members, credits and
repository settings stay in the web app. `enable` exits with its first check's
result; the other account commands exit `0` on success and `2` with the
service's message and remedy otherwise, as `ignore` does.

## Check

```console
slopcop                          # check the change in the current checkout
slopcop check --if-enabled       # automatic check: skip a repository not enabled here
slopcop check --format json      # same, machine-readable
slopcop --root ../repo --base origin/main
```

The base commit defaults to the merge base with the branch upstream, then the
merge base with `origin/HEAD`, then `HEAD` when neither is known. When the
upstream merge base yields an empty patch, as on a branch whose commits are all
pushed, the CLI uses the merge base with `origin/HEAD` instead, so a check
right after a push still covers the branch. `--base REV` names the branch the
change started from and, like `git diff REV...HEAD`, uses the merge base of
`REV` and `HEAD`, so commits that landed on `REV` after the branch point are not
sent as reverse edits. A branch stacked on `REV` that has since merged a newer
`origin/HEAD`, or `REV`'s own upstream, moves the base to that line's merge base
once it holds `REV`'s, as after `REV` landed, so the merged-in commits are not
sent either. When the lines have diverged, no fetchable commit holds both, so
the base stays and the CLI warns that the patch carries the merged-in commits.
`--base-exact REV` diffs against `REV` itself. The
service must already have the base commit in its clone; a missing one comes back as the service's
`base_not_found` message and remedy. The CLI then asks origin for its default
branch (`git ls-remote`, no prompt, 10 second limit) and adds a line when the
clone explains the failure: origin's default branch was renamed and
`origin/HEAD` still names the old one, or the base is not on origin's default
branch.

Every run with a static half sends one check request, even when the working tree
matches the base and the patch is empty. The static result comes from the
service; local review runs separately. A report for an empty patch comes with a
note on stderr that suggests `--base`, and `json` adds `"emptyChange": true`
(`sarif`: `properties["slopcop.emptyChange"]`): a clean result then means
nothing changed was checked.

Flags:

| Flag | Meaning |
| --- | --- |
| `--mode both\|static\|ai` | both halves (the default where reviewers are configured, outside CI), static only (the default otherwise), or review only |
| `--static`, `--ai` | shortcuts for the corresponding mode |
| `--intent TEXT` | user request when no session transcript is found |
| `--review-timeout DURATION` | local review timeout (default `20m`) |
| `--root PATH` | repository to check (default `.`) |
| `--base REV` | branch or revision the change started from; the base is its merge base with `HEAD` |
| `--base-exact REV` | exact base commit or revision, without a merge base |
| `--format human\|json\|sarif` | report format (default `human`) |
| `--fail-on never\|finding\|note\|warning\|error` | exit-1 threshold for new findings (default `finding`) |
| `--certainty definite\|all` | report and fail on only `definite` findings (default), or on `possible` and `heuristic` ones too |
| `--policy PATH` | repository-relative `.rqlp` file to evaluate (repeatable) |
| `--no-policy-discovery` | skip `.slopcop/policies` rules |
| `--timeout DURATION` | how long to wait for the result (default `10m`) |
| `-w`, `--workspace NAME\|ID` | check in, and bill, this workspace of the account instead of the login's |
| `--if-enabled` | check only when the repository is active in the workspace; otherwise exit `0` without sending a check |
| `--add-repository` | add the repository to the workspace `--workspace` names, which then pays for it, without asking at a terminal; for after the user approved it. A check without it never adds a repository |
| `--verbose` | print the resolved request to stderr, and name each inconclusive check in `human` |

### Modes and local review

`slopcop check` runs hosted static analysis and local LLM review concurrently
by default. Use `--mode static` or `--static` for only the hosted check; use
`--mode ai` or `--ai` for only local review. A single captured snapshot
includes committed, uncommitted, and untracked work, except ignored files.
Reviewers read `git diff <baseTree> <snapshotTree>` themselves.

Reviewers, in priority order, are GPT Terra (`terra`) and GPT Sol (`sol`) through
Codex ACP, DeepSeek Flash (`flash`) through dsh ACP, and Claude Haiku (`haiku`)
through Claude ACP. Each uses the newest model of its family at high effort.
`slopcop init` probes each family once when reviewers are not configured and
mode is not static, then saves the first two that work (or one). Configured
reviewers are left alone; unset `review.reviewers` and run init to pick again.
`slopcop init --no-reviewers` skips probing. A check runs exactly the configured
reviewers and fails with exit `2` if one cannot run. Set the ordered list (1–4
entries; duplicates allowed) with `slopcop config set review.reviewers terra,sol`.
Set `review.timeout` (default `20m`) or `mode` with `slopcop config set`;
adapter commands are under `review.adapters.<codex|dsh|claude>.command`.
The prompt carries the captured diff. Reviewers read repository content,
which is untrusted. Claude and DeepSeek reviewers may only read and search
files; Slopcop refuses their requests to run commands or change files. Codex
reviewers run in Codex's read-only sandbox.
CI normally uses `--mode static`.

Review state and logs are under `<git-dir>/slopcop/`. A complete review is
reused when nothing relevant changed. A nonempty check with a clean review
and a completed static result with no new findings at or above
`gate.blockOn` (default `error`) records `refs/slopcop/reviewed/<tree>`.
Warnings and notes are advisory for push approval, though they still exit `1`
by default. The Claude Code gate checks HEAD's tree, so
after checking a dirty tree, commit exactly that state. For demonstrably wrong
complete review findings, `slopcop override --reason "..."` records acceptance
when the static half has no findings at or above the push level
and completed, was skipped, or did not run. Never override real bugs or failed or incomplete reviews.

### What is sent

In modes with the static half, the CLI sends one git patch from the base commit
to the working tree. An empty change is sent as an empty patch, not filtered
out. The patch carries committed but unpushed work, unstaged edits, untracked
files, deletions, renames, mode changes, and binary content. Git-ignored files
stay out, except selected `.slopcop/policies` rules, which must travel because
the service evaluates them from the patched checkout.

A check run by a Claude Code, Codex or OpenCode agent also sends which models wrote the
change, so the service can tie each finding to the model that wrote its file.
The CLI finds the agent's session from its environment and reads that
session's own record:

- **Claude Code:** `CLAUDE_CODE_SESSION_ID` names the transcript under
  `~/.claude/projects` (or `$CLAUDE_CONFIG_DIR/projects`), subagents included.
  Each successful Write, Edit, MultiEdit or NotebookEdit gives a model, an
  effort and a file.
- **Codex:** `CODEX_THREAD_ID` names the rollout under `~/.codex/sessions` (or
  `$CODEX_HOME/sessions`). Each completed file change gives the files, and its
  turn gives the model and effort. A move counts as writing its destination.
- **OpenCode:** `OPENCODE_SESSION_ID` names the session in OpenCode's
  database, `~/.local/share/opencode/opencode.db` (or `$OPENCODE_DB`),
  subagents included. Each completed edit, write or patch gives the files,
  and its step gives the model and its variant as the effort.

For static attribution, it reads only those fields, never the conversation, tool
input, or file contents from the session. What it sends is the harness, its
version, the session id as an opaque identifier, and each model and effort with
the repository-relative files it wrote. A check run outside an agent session
sends none of this. It is analytics only, asserted by the client, and never
decides a check. `--verbose` prints it.

The service owns the limits on what a check accepts. When a change is too
large or touches too many files, the service's `payload_too_large` response is
printed verbatim; the CLI does not impose its own limits.

Every check with a static half also runs the hosted catalog. Rules under
`.slopcop/policies/**/*.rqlp` are added automatically unless
`--no-policy-discovery` is set.

### New and pre-existing findings

The static half fails only on findings the change introduced or touched:
the service
marks a finding new when it is in a file the change writes and its lines meet a
line the change added, or the line beside one it removed. Findings that were
already in the code are counted (`summary.existingFindings`, and `N already in
the code` in the human summary), never fail the check, and are not listed. A
change that introduces nothing exits `0`, however many findings the repository
already holds. `--show-existing` is deprecated and hidden: the service no longer
sends those findings, so against it the flag lists nothing (a service from
before that still gets them listed), and it stays so that scripts passing it
keep running.

### Ignore a false positive

Each finding a check reports carries an `id`. When a finding is a false
positive, ignore it:

```console
slopcop ignore 0f8fad5b-d9cb-469f-a165-70867728950e
```

That finding is not reported again for this repository. Ignoring covers that
one finding, its rule plus its place in the code, in its repository for your
workspace; the rule keeps reporting everything else. Ignoring a finding twice
is fine. `ignore` uses the same login and service URL as a check, exits `0`
with a one-line confirmation, and otherwise exits `2` with the service's
message and remedy; `finding_not_found` means the id is unknown or comes from a
check outside your workspace.

To undo a mistaken ignore, run `slopcop unignore <id>` with the id it was
ignored from, or the same finding's id from any later check of the repository.
Later checks of the repository report the finding again. Unignoring a finding
that is not ignored is fine; it exits and fails like `ignore`.

`slopcop ignored` lists the findings your workspace ignored in this repository,
newest first, 25 per page (`--page <n>`), with each one's id for `unignore`,
location, rule, and when and by whom it was ignored. `--format json` prints the
service's answer.

### Output and exit codes

The service answers structured data; the CLI renders it. `human` prints one
block per new finding, with an `ID:` line when the service names one, then a
summary line such as `1 new finding across 1 file(s); 72 already in the code |
Analysis complete`, and any omitted new findings or
incomplete checks. `json` prints `status`, `baseCommit`, `summary`
(`newFindings` and `existingFindings` count the split), `findings` (each with
`new` and its `id`), `incomplete`, `inconclusive`, `failure` when the analysis did not
complete, and `emptyChange` when the patch was empty. When the check
installed or updated the skill, `skillUpdated` lists each skill directory it
wrote: `dir`, `from` (a release, `unknown`, or absent for a fresh install),
`to`, `changed` and, for a skill with local edits, `backup`; the failed and
pending reports carry it too, and `sarif` puts it in
`properties["slopcop.skillUpdated"]`. `sarif` prints a SARIF 2.1.0 log whose results carry `baselineState`
`new` and the finding id in `properties["slopcop.findingId"]`. `--format` only
changes the rendering, never the request, so the service answers the same check
whichever is used.

| Exit | Meaning |
| --- | --- |
| `0` | Check completed; no new finding crossed the `--fail-on` level |
| `1` | A new finding crossed `--fail-on` |
| `2` | No new finding crossed `--fail-on`, but a check was incomplete; or the CLI or service failed |
| `3` | The check was still running when `--timeout` ran out |
| `4` | The service refused the check, and only a person can clear it |
| `130`, `143` | Interrupted before the check answered: `130` for Ctrl-C, `143` for SIGTERM |

Only `clean` exits `0`: a status this CLI does not know exits `2`, so a newer
service can never pass a check by accident, and stderr names the status. An
interrupted check prints no report. Interrupted before it was sent, nothing
reached the service; after, a service that received it keeps running it, and
running the same check again picks up its result.

An inconclusive check, one that ran but could not prove there is nothing
more to find, never changes the exit code or the status. Complete proof is
rare in static analysis, typically at a library boundary, so this is the norm
rather than a gap. Each report still says where they are, so a reviewer can
judge what a `clean` result is worth: `human` prints one `Inconclusive:` line
with the count and how many concern only files the change does not write, and
`--verbose` names each check. `json` lists them in `inconclusive`, each with
`policyId`, `reason` and `elsewhere` (true when it concerns only files the
change does not write), beside the `summary.inconclusive` count. `sarif` adds
a `note` notification per check with `slopcop.inconclusive` and
`slopcop.elsewhere` properties. The service names at most 50; the counts
cover the rest. It does not yet say which files or lines each one concerns,
or what evidence was missing. A check that timed out or
that Bifrost judged unreliable is incomplete and counts wherever it is.

The service applies `--fail-on` and sets the status; the exit code follows it.
The CLI always sends `--fail-on`, defaulting to `finding`: a check fails only
on findings the change introduced, so any of them, a note or an unrated one
included, is worth fixing before the change lands. The service's own default
for a request without `failOn` is Bifrost's `warning`; pass `--fail-on warning`
to match it.
New findings that cross `--fail-on` exit `1` even when some check did not
complete.

While the service answers `check_running`, the CLI waits the `Retry-After` it
names and asks again until `--timeout` runs out. It asks by the `checkId` the
answer carries (`GET /v1/check/{checkId}`), so the change is uploaded once; a
service that sends no `checkId`, or no longer knows it, gets the same request
again, which attaches to the same running check. If it is still running then, the CLI exits `3`, prints the
service's message and remedy on stderr, and in `json` prints
`{"status": "pending", "retryAfter": 5, "message": …, "remedy": …}` on stdout
(`sarif` prints a run with `slopcop.status` `pending`). Running the same check
again picks up the same analysis.

Service failures print the closed contract exactly: message, remedy, and any
install URL. The codes, messages, and remedies are defined by the service in
`server/src/check.rs` of `BrokkAi/bifrost-service`; the CLI does not
rewrite them. The remedies name no client, so the CLI adds its own command
after some: `slopcop login` when the remedy is to sign in, the listing to
choose from after `workspace_not_found` or `workspace_repository_not_found`,
and `slopcop whoami` after `repository_disabled`, since the workspace that
disabled it is the one this login is bound to, and `--workspace` after
`owner_required`. A failed check exits `4` when only a person can clear the
refusal (`auth_required`, `auth_expired`, `account_required`,
`repo_not_indexed`, `app_not_installed`, `app_no_repo_access`,
`gitlab_not_connected`, `no_access`, `insufficient_credits`,
`repository_disabled`, `repository_not_enabled`,
`repository_add_unconfirmed`, `workspace_repository_limit_reached`,
`active_repository_limit_reached`, `owner_required`, and `browser_required`),
so a script can stop instead of retrying, and `2` otherwise. The CLI refuses a
check of a repository the workspace has not added locally, as
`repository_not_enabled`, before sending anything. Stderr starts with the
code, as in `slopcop: repository_not_enabled: …`. `human` prints nothing on
stdout; `json` prints `{"status": "failed", "code": …, "message": …,
"remedy": …, "retryable": …}`, plus `installUrl` or `topUpUrl` when the
service sent one; `sarif` prints a run with `slopcop.status` `failed`, whose
invocation is unsuccessful and carries an `error` notification whose
descriptor id is the code.

## API

The CLI calls `POST <service>/v1/check` with a bearer token:

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

`addRepository` is sent only after a person confirms adding the repository, at
the terminal or with `--add-repository`. The service rejects any other field.
With `--workspace`, every request, the check's, the ignore's and the account
routes', carries the workspace's id in the `SlopCop-Workspace` header; a name
is first looked up with `GET <service>/v1/whoami`. The response carries `status`,
`baseCommit`, `applied`, `summary`, `findings`, `incomplete`, and `failure`
when the analysis did not complete. Against a service from before findings
were marked `new`, the CLI treats every finding as new, as that service gated
on all of them. Non-2xx responses carry `code`, `message`,
`remedy`, `retryable`, and an optional `installUrl`.

Each finding in the response carries an `id`; a service from before findings
could be ignored omits it.

`slopcop ignore <id>` calls `POST <service>/v1/ignore` with the same bearer
token and `{"findingId": "<id>"}`, and gets back `{"findingId": "<id>",
"status": "ignored"}`. Failures use the same closed contract, including
`finding_not_found`. `slopcop unignore <id>` calls `POST <service>/v1/unignore`
the same way and gets back `{"findingId": "<id>", "status": "unignored"}`, also
when the finding was not ignored. `slopcop ignored` calls `GET
<service>/v1/ignores?repoUrl=<url>&page=<n>` (`page` zero-based) and gets back
`repository`, `total`, `page`, `pageSize` and `ignores`, newest first; see the
[API reference](docs/src/content/docs/reference/api.md).

The account commands use the same bearer token. `GET <service>/v1/whoami`
answers `account`, `workspace` (`id`, `name`, `kind`, `role`, `current`),
`plan` (`pro` or `free`) and `workspaces`. `POST <service>/v1/workspace` with
`{"workspaceId"}` rebinds the login. `GET <service>/v1/repositories` answers
`repositories` (`id`, `provider`, `fullName`, `enabled`, `active`) with
`activeRepositoryLimit`, `activeRepositories` and `canManageRepositories`, and
`POST <service>/v1/repositories/state` with `{"repositoryId", "enabled"}`
answers `{repositoryId, fullName, enabled, active}`. Their failure codes add
`account_required`, `workspace_not_found`, `workspace_repository_not_found`,
`owner_required` and `browser_required` (a private Bitbucket or GitLab
repository, which only the web app can change) to the check's.

`slopcop login` uses the device flow: `POST /api/device` returns a
`deviceCode`, `userCode`, `verificationUri`, `expiresIn`, and `interval`, and
`POST /api/token` returns `202` while pending or `200` with a `token` once
approved. The failure codes are `slow_down`, which raises the polling interval,
`expired_token`, `access_denied`, and `device_flow_disabled`. A failure the
service marks `retryable`, such as `rate_limited` or `service_unavailable`,
keeps the code valid, and so do a dropped connection and a proxy's `429`,
`502`, `503` or `504` without a code: login waits the `Retry-After`, or the
interval if that is longer, and polls again until the code expires.

## Development

```console
make test        # go test ./...
make vet         # go vet ./...
make build       # bin/slopcop-<goos>-<goarch>
make cross-check # linux, darwin, and windows on amd64 and arm64
make npm-test    # node --test npm/test
make wheels      # pypi-dist/, one wheel per release target
```

Releases are tag-driven. Pushing a `v*.*.*` tag runs GoReleaser, which builds
linux, darwin, and windows for amd64 and arm64, packages `tar.gz` archives
(`zip` on Windows) with `checksums.txt`, and publishes the GitHub release.
`.goreleaser.yaml` is the single source of truth; CI runs `goreleaser check`
and a snapshot build on every change, and `make release-check` runs the same
locally when GoReleaser is installed. The same tag push then packs
`@brokkai/slopcop` from those release archives and publishes it to npm with
[trusted publishing](docs/src/content/docs/contribute/npm-publishing.md), so the package needs no npm
token.

The same tag also packs a wheel per release target from the binaries GoReleaser
just built, installs the wheel for every platform GitHub can run, and
publishes `brokk-slopcop` to PyPI with
[trusted publishing](docs/src/content/docs/contribute/pypi-publishing.md), so that package needs no PyPI
token either.

Layout:

| Path | Purpose |
| --- | --- |
| `cmd/slopcop` | process entry point |
| `internal/cli` | cobra commands, selection, exit codes |
| `internal/gitx` | remote canonicalization, base resolution, patch building |
| `internal/service` | `/v1/check` client and the wire contract |
| `internal/report` | human, JSON, and SARIF rendering from the structured response |
| `internal/config` | stored service URL and token |
| `internal/hooks` | Claude Code and Codex hook installation and removal |
| `npm` | launcher, packer, and tests of the `@brokkai/slopcop` package |
| `packaging/pypi` | wheel packer and launcher of the `brokk-slopcop` package |
| `skills/slopcop` | the agent skill the binary embeds and installs |
| `docs` | the documentation site at <https://docs.slopcop.com> (Astro Starlight), including the npm and PyPI release flows |
| `.goreleaser.yaml` | tag release build, archives, and checksums |
