# Publishing to npm

The public package is `@brokkai/slopcop` and its executable is `slopcop`. The
tarball carries the Node launcher in `npm/bin/slopcop.cjs` and the release
binary of every supported platform under `native/`, so an installation runs no
scripts and needs no npm dependencies.

`npm/scripts/release.mjs` builds the tarball from the archives of an already
published GitHub release. Every archive is checked against that release's
`checksums.txt` before any of it is staged, so the package can only contain
bytes the release published. `npm/package.json` is the packaging template: the
packer drops `private` and stamps the version the release tag names. Never
publish `npm/` as it stands.

## Release workflow

`.github/workflows/release.yml` runs GoReleaser on a `v*.*.*` tag push and then
its `npm` job. That job downloads the archives of the release GoReleaser just
published, packs the tarball, installs it, checks that the packed binary
reports the released version, and publishes. It uses Node.js 24, npm 11.19.0,
and `id-token: write`; npm exchanges that GitHub OIDC token for short-lived
publishing credentials, so no npm token exists in this repository. Because the
repository and the package are public, npm also attaches provenance to every
published version.

Stable versions publish on the `latest` dist-tag and prereleases on `next`. A
rerun skips a version that already exists only when the tarball integrity
matches exactly; different contents need a new version. After publishing, the
run waits for the registry to expose the version rather than publishing twice.

CI packs and installs a stand-in release (`npm/scripts/fixture-release.mjs`)
on every change, so the packing, installing, and launcher targets are
exercised before a tag can use them.

## First publication and the trusted publisher

npm only accepts a trusted publisher for a package that exists, so the first
version is published by hand, with an account that can write to `@brokkai` and
has 2FA enabled. That account also runs `npm trust`, which needs npm 11.15.0 or
newer.

Until the package exists and its trusted publisher is configured, the npm job
of a tag run fails at its last step, after GoReleaser and after the tarball is
packed and smoke-tested. Nothing is left half-published at that point: the
GitHub release is complete and npm has nothing new. Bootstrap the version that
tag released, configure the publisher, and let the next tag publish through it
(or rerun the failed job from the Actions tab, which publishes the same version
once the publisher is there).

From a clean checkout at the released tag, with that release's archives
downloaded:

```sh
npm login --registry=https://registry.npmjs.org/
gh release download v0.1.0 --repo BrokkAi/slopcop --dir release-assets \
  --pattern 'slopcop_*' --pattern 'checksums.txt'
make npm-pack NPM_TAG=v0.1.0
make npm-smoke NPM_TAG=v0.1.0
npm publish ./npm-dist/brokkai-slopcop-0.1.0.tgz --access public --ignore-scripts
npm trust github @brokkai/slopcop --repo BrokkAi/slopcop --file release.yml --allow-publish
npm trust list @brokkai/slopcop --json
```

Answer npm's browser and 2FA prompts when it asks. The trusted publisher must
name owner `BrokkAi`, repository `slopcop`, workflow filename `release.yml`,
and no environment, because the workflow declares none. It must also allow
`npm publish`: a configuration created today allows staged publishing until
that action is selected, and `--allow-publish` (or the Allowed actions
checkbox on npmjs.com) is what selects it. A new configuration expires two days
after it is created unless it publishes successfully, so add it just before
the next release tag.

Once the first version is out and the publisher is configured, restrict
traditional access: package Settings, Publishing access, "Require two-factor
authentication and disallow tokens". Trusted publishers keep working; only
long-lived tokens stop.

## Recovery

If npm publication fails after the GitHub release succeeded, rerun the same tag
workflow. The rerun packs the same archives again and skips a version whose
contents already match. Never publish different contents under a version that
exists, and never move the tag of a completed release.

See npm's [trusted publishers](https://docs.npmjs.com/trusted-publishers/) and
[`npm trust`](https://docs.npmjs.com/cli/v11/commands/npm-trust/) documentation.
