# SlopCop npm packaging

This directory is the packaging source of the public `@brokkai/slopcop` npm
package. The published tarball carries the small launcher in `bin/`, the Go
binary of every supported platform under `native/`, and the repository's
`README.md` and `LICENSE`.

`scripts/release.mjs` builds that tarball from the archives of an
already-published GitHub release. Every archive is checked against the
release's `checksums.txt` before any of it is staged, so the npm package can
only ever contain bytes that release published. `npm/package.json` is the
packaging template: the packer drops `private` and stamps the version that the
release tag names. Never publish this directory as it stands.

```console
node --test npm/test/
node npm/scripts/release.mjs pack --release-tag v0.1.0 --assets release-assets
```

See [npm publishing](../docs/npm-publishing.md) for the release flow, the
trusted publisher configuration, and the first-publication steps.
