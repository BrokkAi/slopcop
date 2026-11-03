---
title: Development
description: "Build, test, and lay out the slopcop CLI repository."
sidebar:
  order: 1
---

The CLI is Go, in [BrokkAi/slopcop](https://github.com/BrokkAi/slopcop) under
the Apache-2.0 license. Go 1.25 or newer is required.

```console
make test        # go test ./...
make vet         # go vet ./...
make build       # bin/slopcop-<goos>-<goarch>
make install     # go install, with the version stamped from git describe
make cross-check # linux, darwin, and windows on amd64 and arm64
make npm-test    # node --test npm/test
make wheels      # pypi-dist/, one wheel per release target
```

CI runs `make fmt-check`, `make vet`, and `make test` on every change, plus
`goreleaser check` and a snapshot build of every release target, the npm
packaging against a stand-in release, and the PyPI wheels installed on each
platform GitHub can run.

## Layout

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
| `docs` | this site |
| `.goreleaser.yaml` | tag release build, archives, and checksums |

## Principles the code keeps

- **Hosted only.** The analyzer runs in the service, and only there. The CLI
  has no embedded engine, no local mode, and no offline fallback, and must not
  grow one.
- **The service decides.** The CLI renders what the service answers and never
  rewrites a code, message, or remedy. A status it does not know exits `2`.
- **No prompts for agents.** Anything that changes what a workspace pays asks
  at a terminal and refuses without one, so an agent running with your login
  cannot do it for you.
- **One request per run.** An empty change is a real check, not a skip.

## Dogfooding

Run `slopcop check --format json --base origin/master` before calling a change
done, triage the findings, and fix the real ones. The repository's own
`.slopcop/policies` rules, when any, run with every check.
