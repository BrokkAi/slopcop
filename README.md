# SlopCop

**Static analysis deep enough for CI, fast enough to run on every edit.**

[![CI](https://github.com/BrokkAi/slopcop/actions/workflows/ci.yml/badge.svg)](https://github.com/BrokkAi/slopcop/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/BrokkAi/slopcop)](https://github.com/BrokkAi/slopcop/releases/latest)
[![npm](https://img.shields.io/npm/v/@brokkai/slopcop)](https://www.npmjs.com/package/@brokkai/slopcop)
[![PyPI](https://img.shields.io/pypi/v/brokk-slopcop)](https://pypi.org/project/brokk-slopcop/)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25%2B-blue.svg)](https://go.dev/)

`slopcop` sends working-tree snapshots in a git packfile to the
hosted check API at <https://slopcop.com>. The service imports them, runs the
hosted policy catalog plus the repository rules you selected, and returns the
report: every finding with its severity, the file and line that triggered it,
and the certainty behind it. The analysis is
[Bifrost](https://github.com/BrokkAi/bifrost), an open-source static analysis
engine rather than a language model, so a finding traces back to a rule instead
of to a guess.

There is no local analyzer and nothing to install per language: the CLI ships
the change and prints what comes back, and it never decides on its own that
there is nothing to check. The command is built for agents and one-shot CI: no
prompts, one request, and the analyzer's exit contract. When a finding is wrong
for the code it flags, `slopcop report` records it as a false positive: the
finding stops failing that repository's checks in the same workspace (or with
the same operator token), and the team sees the report
and its reason on the SlopCop dashboard.

## Install

npm, PyPI, the release archive, and the PowerShell installer all install the
same release binary for macOS, Linux, and Windows on x64 and arm64, so
`slopcop version` reports which release it came from. The Go toolchain can
build that binary from source instead.

| Channel | Platforms | Needs |
| --- | --- | --- |
| [npm](#npm) | macOS, Linux, Windows | Node 22 or newer |
| [PyPI](#pypi) | macOS, Linux, Windows | Python 3.9 or newer |
| [Release archive](#release-archive) | macOS, Linux, Windows | nothing |
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
slopcop. `slopcop init` installs it into each well-known skills directory whose
harness is present, and names what it did:

| Location | Harness |
| --- | --- |
| `~/.agents/skills/slopcop/` | Codex, opencode |
| `~/.claude/skills/slopcop/` | Claude Code, opencode |
| `$CODEX_HOME/skills/slopcop/`, or `~/.codex/skills/slopcop/` | Codex home |

A skill an earlier release installed, untouched, is brought up to date, so an
agent learns what the new release can do. A skill with local changes is left
alone and init says so, so local edits survive and running init again is
safe. `slopcop init --no-skill` skips the install and any update of it.

Each file is written to a temporary file, synced, and renamed into place, so
an interrupted init never leaves a truncated skill; a replaced file keeps its
permissions, and one that already matches is not rewritten. A skill file that
is a symlink (a dotfile manager's, say) is resolved and its target replaced
the same way, so the link stays a link. A link whose target does not exist yet
gets it created where it points, if that directory exists. Init goes on past a
directory it cannot read or write and names it. It exits `2` when a harness is left without a
working skill: none could be installed, or its `SKILL.md` cannot be read (so
the harness cannot read it either). A skill with another file init cannot
read is left alone, as one with local changes is, and init warns and exits
`0`. So does an earlier release's skill it cannot update, such as a read-only
file or a link into a read-only directory: a harness can still read it, though
it may be partly updated, so init says to run it again once it is writable.

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
and the snapshot has no changed files. A clean result or a failure always comes from the
service.

Flags:

| Flag | Meaning |
| --- | --- |
| `--root PATH` | repository to check (default `.`) |
| `--base REV` | base commit or revision for the change |
| `--rule ID`, `--skip-rule ID` | include or exclude a rule by ID (repeatable) |
| `--category NAME`, `--skip-category NAME` | include default rules in a category or exclude a category (repeatable) |
| `--since REV` | compare since a commit-ish or `merge-base` (default: last completed branch run, else BASE) |
| `--format human\|json\|sarif` | report format (default `human`) |
| `--fail-on never\|finding\|info\|note\|warning\|error` | exit-1 threshold for new findings (default `finding`) |
| `--show-existing` | also list the findings already in the code and those reported as false positives |
| `--policy PATH` | repository-relative `.rqlp` file to evaluate (repeatable) |
| `--no-policy-discovery` | skip `.slopcop/policies` rules |
| `--timeout DURATION` | how long to wait for the result (default `10m`) |
| `--verbose` | print the resolved request to stderr |

### What is sent

A git packfile contains deterministic NOW and SINCE snapshot commits, each
anchored directly to BASE as its only parent. The pack excludes objects already
reachable from BASE and all intermediate branch history. NOW includes committed
but unpushed work, unstaged edits, untracked non-ignored files, deletions, modes,
and binary content. Selected policy files travel even when ignored. The user's
index is untouched, and even an unchanged working tree sends a snapshot.

`--since` accepts a commit-ish or `merge-base` (the resolved BASE). By default,
it uses `refs/slopcop/last-run/<branch>`, else BASE. The selected tree is
re-anchored to the current BASE; when it matches BASE's tree, BASE itself is
reused. A completed run (including findings or incomplete analysis) advances
the branch ref to NOW. Rejected requests, pending checks, transport errors,
and timeouts do not advance it. Detached HEAD runs have no last-run marker.
Use `--since merge-base` for a complete branch comparison in CI.

The decoded packfile is limited to 16 MiB; larger packs fail locally before
sending a request. Other service limits and remedies are printed verbatim.
Rules under `.slopcop/policies/**/*.rqlp` are added automatically unless
`--no-policy-discovery` is set. Explicit `--policy PATH` files remain supported.

Without `--rule` or `--category` includes, the default rules run. Otherwise,
the service runs the union of named rules and the default rules in named
categories. Then `--skip-rule` and `--skip-category` remove rules by ID or
category. Unknown names are an error. All four flags are repeatable; duplicate
values are sent once. There is no pack selector. Explicit `--policy PATH`
files and discovered repository policies continue to join the check.

### New, pre-existing, and dismissed findings

A check fails only on findings the change introduced or touched: the service
marks a finding new when it is in a file the change writes and its lines meet a
line the change added, or the line beside one it removed. Findings that were
already in the code are counted, never fail the check, and are listed only with
`--show-existing`. A change that introduces nothing exits `0`, however many
findings the repository already holds.

A finding reported as a false positive in the repository is dismissed: the
service sends it with `new: false` and `dismissed: true`, counts it in
`summary.dismissedFindings`, and never lets it decide the status. Like the
pre-existing findings, dismissed ones are always counted and listed only with
`--show-existing`, except in `sarif`, which always carries them as suppressed
results (see below).

### Output and exit codes

The service answers structured data; the CLI renders it. `human` prints one
block per new finding, each with its `Id:` line, and after them one line on
how to report a wrong one:

```text
New findings
  [warning] bifrost.performance.regex-compile-in-loop: Regex compiled inside a loop
    Location: server/domain.rs:703
    Certainty: definite
    Id: 6f1c2a9e-3b4d-4e5f-8a7b-0c1d2e3f4a5b

False positive? slopcop report <id> --reason "why"

1 new finding across 1 file(s); 72 already in the code; 2 reported as false positive (--show-existing lists them) | Analysis complete
```

A finding id exists only on the service that ran the check, so when the check
named its service with `--service` or `$SLOPCOP_SERVICE`, that line names it
too, as `slopcop --service URL report <id> …` with any credentials dropped
from the URL: the copied command still works where the variable is not set.
A service stored by `slopcop login` is not named, since a report finds it on
its own.

With `--show-existing` the pre-existing findings follow under `Already in the
code`, then the dismissed ones under `Reported as false positive`; neither
ever fails the check. Any omitted or incomplete checks come after the summary
line.

`json` prints `status`, `checkId`, `baseCommit`, `summary` (`newFindings`,
`existingFindings`, and `dismissedFindings` count the split), `findings` (each
with `id`, `new`, and `dismissed`), `incomplete`, and `failure` when the
analysis did not complete. `sarif` prints a SARIF 2.1.0 log whose run carries
`properties["slopcop.checkId"]` and whose results carry `properties["slopcop.findingId"]` and
`baselineState`: `new` for a new finding, `unchanged` for one already in the
code or reported as a false positive, so a gate on `baselineState == "new"`
agrees with the exit code. Pre-existing findings are results only with
`--show-existing`, but a dismissed finding is always one, with a
`suppressions` entry of kind `external` justified as `reported as false
positive`, so code scanning shows a reported finding as suppressed rather
than closing it as fixed. Against a service from before checks had ids, `checkId` and `id`
are empty in `json`, absent in `sarif`, and `human` prints no `Id:` line or
report hint.

`--format` and `--show-existing` only change the rendering, never the
request, so the service answers the same check whichever is used.

| Exit | Meaning |
| --- | --- |
| `0` | Check completed; no new finding crossed the `--fail-on` level |
| `1` | A new finding crossed `--fail-on` |
| `2` | No new finding crossed `--fail-on`, but a check was incomplete; or the CLI or service failed |
| `3` | The check was still running when `--timeout` ran out |

An inconclusive check fails the check only when it concerns a file the
change writes. One about other files is listed on an `Inconclusive outside
the change` line (`inconclusiveElsewhere` in `json`, a `note` notification in
`sarif`) and never changes the exit code. A check that timed out counts
wherever it is. The service can place an inconclusive check only once Bifrost
reports which files it concerns; until then every one counts as in the change.

The service applies `--fail-on` and sets the status; the exit code follows it.
The CLI always sends `--fail-on`, defaulting to `finding` (note or above).
Severity order is info, note, warning, error. Info findings appear in human
and JSON reports but do not fail `--fail-on finding`; `--fail-on info` gates
them too. `never` disables finding failures. The service's own default for a
request without `failOn` is `warning`.
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
install or top-up URL, with control and bidirectional characters replaced so a
response cannot drive the terminal. A rate-limited (`429`) check or report adds
how long the service asks to wait, from its `Retry-After` in seconds or as an
HTTP date. The codes, messages, and remedies are defined by the service in
`server/src/check.rs` of `BrokkAi/bifrost-service`; the CLI does not
rewrite them. Most remedies name no client, so when `auth_required`,
`auth_expired`, or `device_flow_disabled` says to sign in, the CLI adds the
`slopcop login` command after it. A failed request prints no report on
stdout.

A check never waits on SlopCop indexing the repository, and no longer needs
the repository analyzed on SlopCop first. `repo_not_indexed` keeps its name,
but now means this login cannot check the repository: a private one needs the
SlopCop GitHub App installed with access to it, and a `slopcop login` that can
use that installation. A service from before this change may still answer
`indexing_in_progress`; the CLI prints it and exits `2` without waiting or
retrying.

## Report a false positive

```console
slopcop report 6f1c2a9e-3b4d-4e5f-8a7b-0c1d2e3f4a5b \
  --reason "parse() returns early on empty input (line 12), so the index on line 30 is never out of range"
```

`slopcop report` records a finding from a check as a false positive. The id is
the finding's `Id:` line in `human`, its `id` in `json`, or its result's
`properties["slopcop.findingId"]` in `sarif`. A reported finding stops failing checks of
its repository in the same workspace (or with the same operator token): a later check that would flag it as new sends it as dismissed instead, so an agent's check-and-fix
loop can end on a finding that is wrong. Every report and its reason is
visible to the team on the SlopCop dashboard, an owner can reinstate the
finding, and false-positive rates are tracked per rule, so a report is for a
finding that is wrong, never for one that is merely hard to fix.

| Flag | Meaning |
| --- | --- |
| `--reason TEXT` | why the finding is wrong for this code, 1 to 2000 characters after trimming (required) |
| `--format human\|json` | output format (default `human`) |
| `--service URL` | the service, resolved as for a check |

The CLI checks that the id is a UUID and the reason is in bounds before it
sends anything, and signs in exactly as a check does. `human` says which
finding and rule were recorded:

```text
slopcop: reported finding 6f1c2a9e-3b4d-4e5f-8a7b-0c1d2e3f4a5b (bifrost.correctness.index-out-of-range) as a false positive
It no longer fails checks of this repository in the same workspace (or with the same operator token), and the team sees the report and its reason on the SlopCop dashboard.
```

`json` prints the service's answer: `reportId`, `findingId`, `checkId`,
`policyId`, `kind`, and `status`.

| Exit | Meaning |
| --- | --- |
| `0` | The report was recorded (`recorded`), or the finding was already reported (`already_reported`) |
| `2` | The finding was not reported: the id or reason was invalid, or the service refused or failed |

Failures print the service's message and remedy verbatim, as for a check.
`finding_not_found` means the id names no finding this login can see, and
`invalid_report` that the service rejected the report body.

## API

The CLI calls `POST <service>/v1/check` with a bearer token:

```json
{
  "contractVersion": "check-v2",
  "sinceCommit": "<full SINCE SHA>",
  "changeCommit": "<full NOW SHA>",
  "repoUrl": "https://github.com/owner/name",
  "baseCommit": "0123456789abcdef0123456789abcdef01234567",
  "change": "<base64 git packfile>",
  "selection": {
    "files": [".slopcop/policies/rule.rqlp"],
    "policyIds": ["example.rule"],
    "skipPolicyIds": ["example.skipped-rule"],
    "categories": ["security"],
    "skipCategories": ["performance"]
  },
  "failOn": "finding"
}
```

Empty selection arrays are omitted; an empty selection is omitted entirely.
The service rejects any other field. The response carries `status`,
`checkId`, `baseCommit`, `applied`, `summary`, `findings` (each with `id`,
`new`, and `dismissed`), `incomplete`, and `failure` when the analysis did not
complete. Against a service from before findings were marked `new`, the CLI
treats every finding as new, as that service gated on all of them; against
one from before ids and dismissals, `checkId` and each `id` are empty and no
finding is dismissed. Non-2xx responses carry `code`, `message`, `remedy`,
`retryable`, and an optional `installUrl` or `topUpUrl`. A workspace whose
credits ran out past its grace period answers `402 insufficient_credits` with
a `topUpUrl`; its free repository is still checked.

`slopcop report` calls `POST <service>/v1/report` with the same bearer token:

```json
{"findingId": "6f1c2a9e-3b4d-4e5f-8a7b-0c1d2e3f4a5b", "kind": "false_positive", "reason": "<1-2000 characters>"}
```

A `200` answers `reportId`, `findingId`, `checkId`, `policyId`, `kind`, and
`status` (`recorded` or `already_reported`). Failures use the same closed
shape, with `auth_required`, `auth_expired`, `finding_not_found`,
`invalid_report`, `rate_limited`, and `service_unavailable`.

`slopcop login` uses the device flow: `POST /api/device` returns a
`deviceCode`, `userCode`, `verificationUri`, `expiresIn`, and `interval`, and
`POST /api/token` returns `202` while pending or `200` with a `token` once
approved. The polling codes are `authorization_pending`, `slow_down`,
`expired_token`, and `access_denied`.

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
| `internal/gitx` | remote canonicalization, base resolution, snapshot pack building |
| `internal/service` | `/v1/check` and `/v1/report` client and the wire contract |
| `internal/report` | human, JSON, and SARIF rendering of checks, and of reports |
| `internal/config` | stored service URL and token |
| `internal/terminal` | keeps hostile text off the terminal |
| `npm` | launcher, packer, and tests of the `@brokkai/slopcop` package |
| `packaging/pypi` | wheel packer and launcher of the `brokk-slopcop` package |
| `skills/slopcop` | the agent skill the binary embeds and installs |
| `docs/npm-publishing.md` | npm release flow and trusted publisher setup |
| `docs/pypi-publishing.md` | PyPI release flow and trusted publisher setup |
| `.goreleaser.yaml` | tag release build, archives, and checksums |
