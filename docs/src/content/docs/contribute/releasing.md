---
title: Releasing
description: "Releases are tag-driven. One tag builds the binaries, publishes the GitHub release, and publishes the npm and PyPI packages from those same bytes."
sidebar:
  order: 2
---

Releases are tag-driven. Pushing a `v*.*.*` tag runs GoReleaser, which builds
linux, darwin, and windows for amd64 and arm64, packages `tar.gz` archives
(`zip` on Windows) with `checksums.txt`, and publishes the GitHub release.
`.goreleaser.yaml` is the single source of truth; CI runs `goreleaser check`
and a snapshot build on every change, and `make release-check` runs the same
locally when GoReleaser is installed.

The same tag push then:

- packs `@brokkai/slopcop` from those release archives, checked against
  `checksums.txt`, and publishes it to npm with trusted publishing, so the
  package needs no npm token. See [Publishing to npm](/contribute/npm-publishing/).
- packs a wheel per release target from the binaries GoReleaser just built,
  installs the wheel on every platform GitHub can run, and publishes
  `brokk-slopcop` to PyPI with trusted publishing. See
  [Publishing to PyPI](/contribute/pypi-publishing/).
- for a stable version, fast-forwards `production` to the tagged commit, which
  deploys docs.slopcop.com.

Stable versions publish on npm's `latest` dist-tag and prereleases on `next`;
PyPI takes the version from the tag.

## Cutting a release

```console
git tag v0.1.0
git push origin v0.1.0
```

Then watch the Release workflow. A rerun of the npm or PyPI job skips a version
or file that already exists, so a half-finished publish can be finished by
rerunning.

## What a release changes for users

- The service's `SlopCop-Latest-Version` header starts naming the new release,
  and older CLIs print the [upgrade notice](/get-started/install/#upgrade-notices).
- The next `slopcop login` or check from the new binary updates the installed
  [agent skill](/agents/skill/) and names the files that changed.
- A stable tag fast-forwards the `production` branch to the tagged commit and
  redeploys this documentation from it, so the site describes the released
  CLI; see [This documentation](/contribute/docs/#deploy). The release fails
  at that step if `production` is not an ancestor of the tag, which means
  `production` was moved off `master` and needs reconciling by hand.
