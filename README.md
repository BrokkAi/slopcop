# SlopCop

SlopCop checks a change through the yet to be deployed hosted SlopCop check
API. The CLI ships the working-tree change to the service, the service applies
it to the base commit and runs the hosted policy catalog plus any repository
rules selected, and the CLI prints the report. There is no local analyzer: a check never needs a
Bifrost install, and the CLI never decides on its own that there is nothing to
check.

The command is built for agents and one-shot CI: no prompts, one request, and
the analyzer's exit contract.

## Install

```console
make install
```

or:

```console
go install github.com/BrokkAi/slopcop/cmd/slopcop@latest
```

Go 1.25 or newer is required to build.

The npm package carries the release binary for macOS, Linux, and Windows on x64
and arm64, so it needs no Go toolchain:

```console
npm install --global @brokkai/slopcop
npx @brokkai/slopcop --help
```

Node 22 or newer is required to run that package.

## Enable it in an agent

The binary carries the agent skill that teaches a coding agent how to run
slopcop. Every run installs it, and `slopcop init` does the same install and
names what it did. The skill goes into each well-known skills directory whose
harness is present:

| Location | Harness |
| --- | --- |
| `~/.agents/skills/slopcop/` | Codex, opencode |
| `~/.claude/skills/slopcop/` | Claude Code, opencode |
| `$CODEX_HOME/skills/slopcop/`, or `~/.codex/skills/slopcop/` | Codex home |

A skill that is already there is left alone, so local edits survive and running
init again is safe.

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
and the patch is empty. A clean result or a failure always comes from the
service.

Flags:

| Flag | Meaning |
| --- | --- |
| `--root PATH` | repository to check (default `.`) |
| `--base REV` | base commit or revision for the change |
| `--format human\|json\|sarif` | report format (default `human`) |
| `--fail-on never\|finding\|note\|warning\|error` | exit-1 threshold (default `finding`) |
| `--policy PATH` | repository-relative `.rqlp` file to evaluate (repeatable) |
| `--no-policy-discovery` | skip `.slopcop/policies` rules |
| `--timeout DURATION` | one check request timeout (default `10m`) |
| `--verbose` | print the resolved request to stderr |

### What is sent

One git patch from the base commit to the working tree. An empty change is
sent as an empty patch, not filtered out. The patch carries committed but
unpushed work, unstaged edits, untracked files, deletions, renames, mode
changes, and binary content. Git-ignored files stay out, except selected
`.slopcop/policies` rules, which must travel because the service evaluates them
from the patched checkout.

The service owns the limits on what a check accepts. When a change is too
large or touches too many files, the service's `payload_too_large` response is
printed verbatim; the CLI does not impose its own limits.

Every check also runs the hosted catalog. Rules under
`.slopcop/policies/**/*.rqlp` are added automatically unless
`--no-policy-discovery` is set.

### Output and exit codes

The service answers structured data; the CLI renders it. `human` prints one
block per finding, a summary line, and any omitted or incomplete checks. `json`
prints `status`, `baseCommit`, `summary`, `findings`, `incomplete`, and
`failure` when the analysis did not complete. `sarif` prints a SARIF 2.1.0
log.

| Exit | Meaning |
| --- | --- |
| `0` | Check completed; nothing crossed the `--fail-on` level |
| `1` | A finding crossed `--fail-on` |
| `2` | The check was incomplete, or the CLI or service failed |

The service applies `--fail-on` and sets the status; the exit code follows it.

Service failures print the closed contract exactly: message, remedy, and any
install URL. The codes, messages, and remedies are defined by the service in
`server/src/check.rs` of `BrokkAi/bifrost-service`; the CLI does not
rewrite them. The remedies name no client, so when one says to sign in the CLI
adds the `slopcop login` command after it. A failed request prints no report
on stdout.

While SlopCop is still indexing the repository, the service answers
`indexing_in_progress` at once instead of running the check. The CLI prints it
and exits `2` without waiting or retrying.

## API

The CLI calls `POST <service>/v1/check` with a bearer token:

```json
{
  "repoUrl": "https://github.com/owner/name",
  "baseCommit": "0123456789abcdef0123456789abcdef01234567",
  "change": "<base64 git patch>",
  "selection": {"files": [".slopcop/policies/rule.rqlp"]},
  "failOn": "finding"
}
```

The service rejects any other field. The response carries `status`,
`baseCommit`, `applied`, `summary`, `findings`, `incomplete`, and `failure`
when the analysis did not complete. Non-2xx responses carry `code`, `message`,
`remedy`, `retryable`, and an optional `installUrl`.

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

Layout:

| Path | Purpose |
| --- | --- |
| `cmd/slopcop` | process entry point |
| `internal/cli` | cobra commands, selection, exit codes |
| `internal/gitx` | remote canonicalization, base resolution, patch building |
| `internal/service` | `/v1/check` client and the wire contract |
| `internal/report` | human, JSON, and SARIF rendering from the structured response |
| `internal/config` | stored service URL and token |
| `npm` | launcher, packer, and tests of the `@brokkai/slopcop` package |
| `skills/slopcop` | the agent skill the binary embeds and installs |
| `docs/npm-publishing.md` | npm release flow and trusted publisher setup |
| `.goreleaser.yaml` | tag release build, archives, and checksums |
