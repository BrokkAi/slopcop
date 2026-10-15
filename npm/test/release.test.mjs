import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';

import {
  ROOT_PACKAGE,
  TARGETS,
  archiveNames,
  distTag,
  pack,
  parseChecksums,
  payloadPath,
  publish,
  registryVersion,
  selectArchive,
  stage,
  versionFromTag,
} from '../scripts/release.mjs';
import { buildFixtureRelease, standInBinary } from '../scripts/fixture-release.mjs';

const TAG = 'v1.2.3';
const VERSION = '1.2.3';
const REPOSITORY = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');

function temporary(t, prefix) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), prefix));
  // Windows can briefly keep an exited child's files locked (EPERM/EBUSY).
  t.after(() => fs.rmSync(root, { recursive: true, force: true, maxRetries: 10, retryDelay: 100 }));
  return root;
}

function binaryContent(target) {
  return Buffer.from(standInBinary(TAG, target));
}

function buildRelease(t) {
  const directory = temporary(t, 'slopcop-release-assets-');
  return { directory, archives: buildFixtureRelease(directory, TAG) };
}

// npm writes its cache and logs outside the repository, so keep them beside
// the temporary fixtures instead of the machine's shared cache.
function npmCache(t) {
  process.env.npm_config_cache = temporary(t, 'slopcop-npm-cache-');
}

// Anything other than a matching tag push must not publish, so a test that
// exercises the rest of publish runs as a local run does, without the GitHub
// environment CI itself sets.
function outsideActions(t) {
  const keys = ['GITHUB_ACTIONS', 'GITHUB_EVENT_NAME', 'GITHUB_REF'];
  const previous = keys.map((key) => [key, process.env[key]]);
  t.after(() => {
    for (const [key, value] of previous) {
      if (value === undefined) delete process.env[key];
      else process.env[key] = value;
    }
  });
  for (const key of keys) delete process.env[key];
}

test('release tags and dist tags follow the prerelease convention', () => {
  assert.equal(versionFromTag('v1.2.3'), VERSION);
  assert.equal(versionFromTag('1.2.3'), VERSION);
  assert.equal(versionFromTag('v0.2.0-rc.1'), '0.2.0-rc.1');
  assert.equal(distTag('1.2.3'), 'latest');
  assert.equal(distTag('1.2.3-rc.1'), 'next');
  for (const tag of ['', 'main', 'v1.2', 'v1.2.3/4', undefined]) {
    assert.throws(() => versionFromTag(tag), /is not v<major>\.<minor>\.<patch>/);
  }
});

test('archives resolve under either tag spelling and refuse ambiguity', () => {
  const linux = TARGETS[2];
  assert.deepEqual(archiveNames(TAG, linux), [
    'slopcop_v1.2.3_linux_amd64.tar.gz',
    'slopcop_1.2.3_linux_amd64.tar.gz',
  ]);
  assert.equal(selectArchive(['slopcop_v1.2.3_linux_amd64.tar.gz'], TAG, linux), 'slopcop_v1.2.3_linux_amd64.tar.gz');
  assert.equal(selectArchive(['slopcop_1.2.3_linux_amd64.tar.gz'], TAG, linux), 'slopcop_1.2.3_linux_amd64.tar.gz');
  assert.throws(() => selectArchive([], TAG, linux), /expected exactly one linux\/amd64 archive/);
  assert.throws(() => selectArchive([
    'slopcop_v1.2.3_linux_amd64.tar.gz',
    'slopcop_1.2.3_linux_amd64.tar.gz',
  ], TAG, linux), /found slopcop_v1\.2\.3_linux_amd64\.tar\.gz and slopcop_1\.2\.3_linux_amd64\.tar\.gz/);
  assert.throws(() => selectArchive(['slopcop_v1.2.4_linux_amd64.tar.gz'], TAG, linux), /found none/);
});

test('checksums.txt lines are read strictly', () => {
  const digest = 'a'.repeat(64);
  const sums = parseChecksums(`${digest}  slopcop_v1.2.3_linux_amd64.tar.gz\n${digest.toUpperCase()} *slopcop_v1.2.3_windows_amd64.zip\n\n`);
  assert.equal(sums.get('slopcop_v1.2.3_linux_amd64.tar.gz'), digest);
  assert.equal(sums.get('slopcop_v1.2.3_windows_amd64.zip'), digest);
  assert.throws(() => parseChecksums('not a checksum line'), /is not "<sha256>  <file>"/);
});

test('staging ships only the launcher, the binaries, the README and the LICENSE', async (t) => {
  const { directory: assets } = buildRelease(t);
  const directory = temporary(t, 'slopcop-npm-stage-');
  const manifest = await stage({ assets, tag: TAG, directory });
  assert.equal(manifest.name, ROOT_PACKAGE);
  assert.equal(manifest.version, VERSION);
  assert.equal(manifest.private, undefined);
  assert.equal(manifest.scripts, undefined);
  assert.equal(manifest.dependencies, undefined);
  assert.deepEqual(fs.readdirSync(directory).sort(), ['LICENSE', 'README.md', 'bin', 'native', 'package.json']);
  assert.deepEqual(fs.readdirSync(path.join(directory, 'bin')), ['slopcop.cjs']);
  for (const target of TARGETS) {
    const staged = path.join(directory, ...payloadPath(target));
    assert.deepEqual(fs.readFileSync(staged), binaryContent(target), staged);
  }
  assert.equal(fs.readFileSync(path.join(directory, 'README.md'), 'utf8'),
    fs.readFileSync(path.join(REPOSITORY, 'README.md'), 'utf8'));
  assert.equal(JSON.parse(fs.readFileSync(path.join(directory, 'package.json'), 'utf8')).name, ROOT_PACKAGE);
});

test('packing is deterministic and the tarball carries exactly the package', async (t) => {
  npmCache(t);
  const { directory: assets } = buildRelease(t);
  const first = await pack({ assets, tag: TAG, output: temporary(t, 'slopcop-npm-first-') });
  const second = await pack({ assets, tag: TAG, output: temporary(t, 'slopcop-npm-second-') });
  assert.equal(path.basename(first.archive), `brokkai-slopcop-${VERSION}.tgz`);
  assert.equal(first.integrity, second.integrity);
  assert.deepEqual(fs.readFileSync(first.archive), fs.readFileSync(second.archive));

  const members = execFileSync('tar', ['-tzf', first.archive], { encoding: 'utf8' })
    .split('\n').map((line) => line.trim()).filter(Boolean);
  const expected = ['package/LICENSE', 'package/README.md', 'package/bin/slopcop.cjs', 'package/package.json',
    ...TARGETS.map((target) => `package/${payloadPath(target).join('/')}`)];
  assert.deepEqual(members.sort(), expected.sort());
  if (process.platform !== 'win32') {
    assert.equal(execFileSync('tar', ['-tvzf', first.archive], { encoding: 'utf8' }).includes('-rwxr-xr-x'), true);
  }
});

test('a corrupted or missing release archive stops the pack', async (t) => {
  npmCache(t);
  const { directory: assets, archives } = buildRelease(t);
  const linux = archives.find(({ target }) => target.goos === 'linux' && target.goarch === 'amd64');
  fs.writeFileSync(linux.archive, Buffer.concat([fs.readFileSync(linux.archive), Buffer.from('tampered')]));
  await assert.rejects(pack({ assets, tag: TAG, output: temporary(t, 'slopcop-npm-corrupt-') }),
    /checksums\.txt says/);

  const second = buildRelease(t);
  const windows = second.archives.find(({ target }) => target.goos === 'windows' && target.goarch === 'arm64');
  fs.rmSync(windows.archive);
  await assert.rejects(pack({ assets: second.directory, tag: TAG, output: temporary(t, 'slopcop-npm-missing-') }),
    /expected exactly one windows\/arm64 archive/);
});

test('packing without a release tag fails before packing anything', async (t) => {
  await assert.rejects(pack({ assets: temporary(t, 'slopcop-npm-untagged-'), tag: '', output: temporary(t, 'slopcop-npm-out-') }),
    /pass --release-tag or set \$RELEASE_TAG/);
});

test('automated publication only ever follows a tag push', async (t) => {
  const keys = ['GITHUB_ACTIONS', 'GITHUB_EVENT_NAME', 'GITHUB_REF'];
  const previous = keys.map((key) => [key, process.env[key]]);
  t.after(() => {
    for (const [key, value] of previous) {
      if (value === undefined) delete process.env[key];
      else process.env[key] = value;
    }
  });
  Object.assign(process.env, {
    GITHUB_ACTIONS: 'true',
    GITHUB_EVENT_NAME: 'workflow_dispatch',
    GITHUB_REF: 'refs/heads/master',
  });
  await assert.rejects(publish({
    archive: path.join(temporary(t, 'slopcop-npm-publish-'), 'brokkai-slopcop-1.2.3.tgz'),
    manifest: { name: ROOT_PACKAGE, version: VERSION },
    integrity: 'sha512-identical',
    tag: TAG,
  }), /requires an explicit push of the release tag/);
});

test('an already published version is skipped, a different one is refused', async (t) => {
  outsideActions(t);
  const published = { dist: { integrity: 'sha512-identical' } };
  const stubFetch = async (url) => ({
    status: 200,
    ok: true,
    json: async () => {
      assert.match(url, /^https:\/\/registry\.npmjs\.org\/@brokkai%2Fslopcop\/1\.2\.3$/);
      return published;
    },
  });
  const original = globalThis.fetch;
  globalThis.fetch = stubFetch;
  t.after(() => { globalThis.fetch = original; });
  const log = t.mock.method(console, 'log', () => {});

  const archive = path.join(temporary(t, 'slopcop-npm-published-'), 'brokkai-slopcop-1.2.3.tgz');
  await publish({ archive, manifest: { name: ROOT_PACKAGE, version: VERSION }, integrity: 'sha512-identical', tag: TAG });
  assert.match(log.mock.calls[0].arguments[0], /already published with these contents/);
  await assert.rejects(publish({
    archive,
    manifest: { name: ROOT_PACKAGE, version: VERSION },
    integrity: 'sha512-different',
    tag: TAG,
  }), /already published with different contents/);
});

test('the registry lookup distinguishes missing versions from failures', async (t) => {
  const original = globalThis.fetch;
  t.after(() => { globalThis.fetch = original; });
  globalThis.fetch = async () => ({ status: 404, ok: false, json: async () => ({}) });
  assert.equal(await registryVersion(ROOT_PACKAGE, VERSION), null);
  globalThis.fetch = async () => ({ status: 500, ok: false, json: async () => ({}) });
  await assert.rejects(registryVersion(ROOT_PACKAGE, VERSION), /answered 500/);
});
