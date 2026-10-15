# Publishing to PyPI

The public package is `brokk-slopcop` and its executable is `slopcop`. It is
not `slopcop` because PyPI already has an unrelated project by that name, and
its import package is `brokk_slopcop` for the same reason: two distributions
cannot install the same module. Each platform's wheel carries that platform's
release binary under `brokk_slopcop/_bin/`, so installing one downloads
nothing and the installed command is the same binary as the archives.

Those binaries are built with `CGO_ENABLED=0`, which makes them static: one
file satisfies manylinux (glibc) and musllinux (Alpine) tags alike, so every
Linux binary is published under both. `packaging/pypi/build_wheels.py` writes
the wheels directly — a wheel is a zip with a `<name>-<version>.dist-info`
directory — so packing them needs no packaging backend and no build isolation.

## Release workflow

`.github/workflows/release.yml` runs GoReleaser on a `v*.*.*` tag push and
packs the wheels in that same job, from the binaries in the `dist/` GoReleaser
just built. The `pypi-install` job then installs a wheel on ubuntu-latest,
ubuntu-24.04-arm, macos-latest, and windows-latest, and only after every one
of them answers `slopcop version` does the `pypi` job publish.

Publishing uses the `pypi` environment and `id-token: write`; PyPI exchanges
that GitHub OIDC token for short-lived credentials and signs the uploads with
attestations, so no PyPI token exists anywhere in this repository. A rerun
skips files PyPI already has, so a half-finished upload can be finished by
rerunning the job.

The version comes from the tag: `v1.2.3` publishes `1.2.3`, a prerelease is
normalized to its PEP 440 spelling (`v1.2.3-rc1` becomes `1.2.3rc1`), and a
tag with no PEP 440 spelling fails the job before anything is uploaded. CI
packs the same wheels from a snapshot build on every change, checks that each
wheel resolves on the platform it names, and installs and runs the Linux one,
so a platform whose wheel cannot be packed, resolved, or installed fails
before a tag can use it.

## First publication and the trusted publisher

PyPI accepts a publisher for a project that does not exist yet, so unlike npm
the first release needs no manual upload and no bootstrap version. Add a
"pending" publisher at <https://pypi.org/manage/account/publishing/> with
project name `brokk-slopcop`, owner `BrokkAi`, repository `slopcop`, workflow
`release.yml`, and environment `pypi`. The first run that publishes also
creates the project and turns the pending publisher into a normal one.

A pending publisher does not reserve the name: if another account registers
`brokk-slopcop` before the first publish, the pending publisher is dropped.
Until a publisher exists, a tag's `pypi` job fails at its last step, after the
GitHub release exists and after every install check has passed; the wheels
stay attached to that run as an artifact, so configure the publisher and
rerun the failed jobs.

## Recovery

If the `pypi` job fails after the GitHub release succeeded, rerun the failed
jobs of that tag run: they publish the same wheels again and skip whatever
PyPI already has. Never publish different contents under a version that
exists, and never move the tag of a completed release.

See PyPI's [trusted publishers](https://docs.pypi.org/trusted-publishers/) and
[attestations](https://docs.pypi.org/attestations/) documentation.
