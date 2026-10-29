# SlopCop

**Static analysis deep enough for CI, fast enough to run on every edit.**

[![CI](https://github.com/BrokkAi/slopcop/actions/workflows/ci.yml/badge.svg)](https://github.com/BrokkAi/slopcop/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/BrokkAi/slopcop)](https://github.com/BrokkAi/slopcop/releases/latest)
[![npm](https://img.shields.io/npm/v/@brokkai/slopcop)](https://www.npmjs.com/package/@brokkai/slopcop)
[![PyPI](https://img.shields.io/pypi/v/brokk-slopcop)](https://pypi.org/project/brokk-slopcop/)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25%2B-blue.svg)](https://go.dev/)

`slopcop` sends one git patch, from the base commit to the working tree, to the
hosted check API at <https://slopcop.com>. The service applies it, runs the
hosted policy catalog plus the repository rules you selected, and returns the
report: every finding with its severity, the file and line that triggered it,
and the certainty behind it. The analysis is
[Bifrost](https://github.com/BrokkAi/bifrost), an open-source static analysis
engine rather than a language model, so a finding traces back to a rule instead
of to a guess.

There is no local analyzer and nothing to install per language: the CLI ships
the change and prints what comes back, and it never decides on its own that
there is nothing to check. The command is built for agents and one-shot CI: no
prompts, one request, and the analyzer's exit contract.

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

### Go

```console
go install github.com/BrokkAi/slopcop/cmd/slopcop@latest
```

Go 1.25 or newer is required to build. A plain `go install` reports `dev` as
its version; in a clone, `make install` stamps the version from `git describe`.

## Enable it in an agent

The binary carries the agent skill that teaches a coding agent how to run
slopcop, plus an automatic check hook for Claude Code and Codex. `slopcop init`
installs the skill into each well-known skills directory whose harness is
present, and names what it did:

| Location | Harness |
| --- | --- |
| `~/.agents/skills/slopcop/` | Codex, opencode |
| `~/.claude/skills/slopcop/` | Claude Code, opencode |
| `$CODEX_HOME/skills/slopcop/`, or `~/.codex/skills/slopcop/` | Codex home |

A skill that is already there is left alone by init, so running it again is
safe. `slopcop login` and every check also install the skill in those
directories, rewriting one that differs from the skill bundled into the binary,
so an agent follows the instructions of the slopcop version it runs; local
edits to an installed skill do not survive them. `slopcop init --no-skill`
skips the install and any update of it.

Init also adds a `Stop` hook for each harness installed here, and leaves
everything else in the file as it was:

| File | Harness |
| --- | --- |
| `settings.json` under `CLAUDE_CONFIG_DIR`, or `~/.claude` | Claude Code |
| `hooks.json` under `CODEX_HOME`, or `~/.codex` | Codex |

Codex runs a new or changed hook only once you trust it: open Codex and review
it with `/hooks`. Init never writes that trust itself. The hook stays the same
from one release to the next, so an update does not ask for it again.

When the agent stops, the hook checks the working tree through the same
hosted API, but only when all of these hold:

- the session wrote files in the repository with its edit tools;
- slopcop is signed in, and the workspace a check would use
  (`$SLOPCOP_WORKSPACE`, or the login's) has the repository active
  (`slopcop repo list`), so nothing is sent from other repositories;
- the tree differs from the last check in the session, including a check the
  agent ran by hand.

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

## Sign in

```console
slopcop login                           # browser device flow
slopcop login --token "$SLOPCOP_TOKEN"  # existing credential, for CI and agents
```

The browser flow prints a URL and a code and stores the token once the login is
approved. The service URL defaults to `https://slopcop.com`; `--service`,
`$SLOPCOP_SERVICE`, or a stored value override it.

The credential lives in `slopcop/config.json` under the user config directory
and is written owner-only. `slopcop logout` clears it.

Precedence:

| Value | Order |
| --- | --- |
| Service URL | `--service`, `$SLOPCOP_SERVICE`, stored by `slopcop login`, `https://slopcop.com` |
| Token | `$SLOPCOP_TOKEN`, stored value |
| Workspace (`check`, `ignore`, `repo`) | `-w`/`--workspace`, `$SLOPCOP_WORKSPACE`, the one the login is bound to |

## Workspace and repositories

A login runs its checks and ignores in, and bills, one workspace: the one
active in the browser when the login was approved.

```console
slopcop whoami                     # account, this login's workspace, its plan, the service URL
slopcop workspace list             # the account's workspaces; * marks this login's
slopcop workspace use "Acme team"  # move this login (not the browser) to another, by name or id
slopcop check -w "Acme team"       # check in, and bill, another workspace this once
slopcop repo list                  # the workspace's repositories: active, disabled, or over the limit
slopcop repo disable acme/widgets  # stop checking and charging for one (owner only)
slopcop repo enable acme/widgets   # turn it back on, within the workspace's active limit
```

`whoami`, `workspace list` and `repo list` take `--format human|json`, as do
`workspace use` and `repo enable|disable` for their answer. Every active
repository after a workspace's first costs credits every 30 days; a disabled
one is not checked or charged and keeps its reports, findings and settings.
`repo enable` and `repo disable` ask you to type the repository's `owner/name`
and refuse when stdin is not a terminal: an agent or CI job runs with your
login, and the service cannot tell it from you, so changing what the workspace
pays stays a step you take yourself.
`workspace use` moves every agent and script using this login, not only the
one that runs it, so it asks `[y/N]` and refuses when stdin is not a terminal.
To check in another workspace without moving anyone, `check`, `ignore` and
`repo` take `-w`/`--workspace <name|id>` (or `$SLOPCOP_WORKSPACE`) for that one
command; the login stays where it was. Give agents that share a login their
own `--workspace` instead of switching it.

Names match without regard to case; when two workspaces share a name, or two
providers a repository name, pass the id the listing prints. There is no `repo
add`: a first check adds a repository. When the workspace would then pay for
it (it already has an active repository and is charged), the service refuses
the check with `repository_add_unconfirmed` until a person confirms: at a
terminal `check` prints what it costs, asks `Add owner/name to "Workspace"?
[y/N]`, and on a yes sends the check again; with no terminal, as for an agent
or CI, it exits `2` with the service's remedy. A workspace's first repository
is added without asking. Deleting repositories, archiving
workspaces, members, credits and repository settings stay in the web app. The
commands exit `0` on success and `2` with the service's message and remedy
otherwise, as `ignore` does.

## Check

```console
slopcop                          # check the change in the current checkout
slopcop check --format json      # same, machine-readable
slopcop --root ../repo --base HEAD~1
```

The base commit defaults to the merge base with the branch upstream, then the
merge base with `origin/HEAD`, then `HEAD`. The service must already have the
base commit in its clone; a missing one comes back as the service's
`base_not_found` message and remedy.

Every run sends one check request, even when the working tree matches the base
and the patch is empty. A clean result or a failure always comes from the
service.

Flags:

| Flag | Meaning |
| --- | --- |
| `--root PATH` | repository to check (default `.`) |
| `--base REV` | base commit or revision for the change |
| `--format human\|json\|sarif` | report format (default `human`) |
| `--fail-on never\|finding\|note\|warning\|error` | exit-1 threshold for new findings (default `finding`) |
| `--policy PATH` | repository-relative `.rqlp` file to evaluate (repeatable) |
| `--no-policy-discovery` | skip `.slopcop/policies` rules |
| `--timeout DURATION` | how long to wait for the result (default `10m`) |
| `-w`, `--workspace NAME\|ID` | check in, and bill, this workspace of the account instead of the login's |
| `--verbose` | print the resolved request to stderr |

### What is sent

One git patch from the base commit to the working tree. An empty change is
sent as an empty patch, not filtered out. The patch carries committed but
unpushed work, unstaged edits, untracked files, deletions, renames, mode
changes, and binary content. Git-ignored files stay out, except selected
`.slopcop/policies` rules, which must travel because the service evaluates them
from the patched checkout.

A check run by a Claude Code or Codex agent also sends which models wrote the
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

It reads only those fields, never the conversation, tool input or file
contents. What it sends is the harness, its version, the session id as an
opaque identifier, and each model and effort with the repository-relative
files it wrote. A check run outside an agent session sends none of this. It
is analytics only, asserted by the client, and never decides a check.
`--verbose` prints it.

The service owns the limits on what a check accepts. When a change is too
large or touches too many files, the service's `payload_too_large` response is
printed verbatim; the CLI does not impose its own limits.

Every check also runs the hosted catalog. Rules under
`.slopcop/policies/**/*.rqlp` are added automatically unless
`--no-policy-discovery` is set.

### New and pre-existing findings

A check fails only on findings the change introduced or touched: the service
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

### Output and exit codes

The service answers structured data; the CLI renders it. `human` prints one
block per new finding, with an `ID:` line when the service names one, then a
summary line such as `1 new finding across 1 file(s); 72 already in the code |
Analysis complete`, and any omitted new findings or
incomplete checks. `json` prints `status`, `baseCommit`, `summary`
(`newFindings` and `existingFindings` count the split), `findings` (each with
`new` and its `id`), `incomplete`, and `failure` when the analysis did not
complete. `sarif` prints a SARIF 2.1.0 log whose results carry `baselineState`
`new` and the finding id in `properties["slopcop.findingId"]`. `--format` only
changes the rendering, never the request, so the service answers the same check
whichever is used.

| Exit | Meaning |
| --- | --- |
| `0` | Check completed; no new finding crossed the `--fail-on` level |
| `1` | A new finding crossed `--fail-on` |
| `2` | No new finding crossed `--fail-on`, but a check was incomplete; or the CLI or service failed |
| `3` | The check was still running when `--timeout` ran out |
| `130`, `143` | Interrupted before the check answered: `130` for Ctrl-C, `143` for SIGTERM |

Only `clean` exits `0`: a status this CLI does not know exits `2`, so a newer
service can never pass a check by accident, and stderr names the status. An
interrupted check prints no report. Interrupted before it was sent, nothing
reached the service; after, a service that received it keeps running it, and
running the same check again picks up its result.

An inconclusive check, one that ran but could not prove there is nothing
more to find, never changes the exit code and is not named in any format.
Complete proof is rare in static analysis, typically at a library boundary,
so this is the norm rather than a gap. `summary.inconclusive` counts them in
`json`, as does `slopcop.inconclusive` in `sarif`. A check that timed out or
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
names and asks again until `--timeout` runs out; every ask attaches to the same
running check. If it is still running then, the CLI exits `3`, prints the
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
`owner_required`. A failed request prints no
report on stdout.

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

`addRepository` is sent only after a person confirms adding the repository. The
service rejects any other field. With `--workspace`, every request, the
check's, the ignore's and the account routes', carries the workspace's id in
the `SlopCop-Workspace` header; a name is first looked up with
`GET <service>/v1/whoami`. The response carries `status`,
`baseCommit`, `applied`, `summary`, `findings`, `incomplete`, and `failure`
when the analysis did not complete. Against a service from before findings
were marked `new`, the CLI treats every finding as new, as that service gated
on all of them. Non-2xx responses carry `code`, `message`,
`remedy`, `retryable`, and an optional `installUrl`.

Each finding in the response carries an `id`; a service from before findings
could be ignored omits it.

`slopcop ignore <id>` calls `POST <service>/v1/ignore` with the same bearer
token and `{"findingId": "<id>"}`, and gets back
`{"findingId": "<id>", "status": "ignored"}`. Failures use the same closed
contract, including `finding_not_found`.

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
[trusted publishing](docs/npm-publishing.md), so the package needs no npm
token.

The same tag also packs a wheel per release target from the binaries GoReleaser
just built, installs the wheel for every platform GitHub can run, and
publishes `brokk-slopcop` to PyPI with
[trusted publishing](docs/pypi-publishing.md), so that package needs no PyPI
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
| `docs/npm-publishing.md` | npm release flow and trusted publisher setup |
| `docs/pypi-publishing.md` | PyPI release flow and trusted publisher setup |
| `.goreleaser.yaml` | tag release build, archives, and checksums |
