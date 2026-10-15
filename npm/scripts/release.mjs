#!/usr/bin/env node

// Packs, and optionally publishes, the @brokkai/slopcop npm package from the
// archives of an already published GitHub release. Only the launcher, the
// release binaries, and the repository's README and LICENSE travel in the
// tarball, and every binary is checked against the release's checksums.txt
// first, so the package can only contain bytes that release published.

import { createHash } from 'node:crypto';
import { execFile, spawn } from 'node:child_process';
import { chmod, cp, mkdir, mkdtemp, readFile, readdir, rm, stat, writeFile } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { promisify } from 'node:util';
import { fileURLToPath } from 'node:url';

const execFileAsync = promisify(execFile);
const NPM_DIRECTORY = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const REPOSITORY = path.resolve(NPM_DIRECTORY, '..');
const REGISTRY = 'https://registry.npmjs.org/';
const VERIFY_ATTEMPTS = 61;
const VERIFY_INTERVAL_MS = 5000;
const TAG_PATTERN = /^v?(\d+\.\d+\.\d+(?:-[0-9A-Za-z.]+)*)$/;

export const ROOT_PACKAGE = '@brokkai/slopcop';

// One archive per GoReleaser target, in the layout the launcher resolves.
export const TARGETS = [
  { goos: 'darwin', goarch: 'amd64', extension: 'tar.gz' },
  { goos: 'darwin', goarch: 'arm64', extension: 'tar.gz' },
  { goos: 'linux', goarch: 'amd64', extension: 'tar.gz' },
  { goos: 'linux', goarch: 'arm64', extension: 'tar.gz' },
  { goos: 'windows', goarch: 'amd64', extension: 'zip' },
  { goos: 'windows', goarch: 'arm64', extension: 'zip' },
];

function fail(message) {
  throw new Error(message);
}

// The release tag names the npm version; a prerelease tag stays a prerelease.
export function versionFromTag(tag) {
  const match = TAG_PATTERN.exec(tag ?? '');
  if (!match) {
    fail(`release tag ${JSON.stringify(tag)} is not v<major>.<minor>.<patch>[-prerelease]; ` +
      'pass --release-tag or set $RELEASE_TAG');
  }
  return match[1];
}

export function distTag(version) {
  return version.includes('-') ? 'next' : 'latest';
}

export function binaryName(goos) {
  return goos === 'windows' ? 'slopcop.exe' : 'slopcop';
}

// The path of one target's binary inside the package.
export function payloadPath(target) {
  return ['native', `${target.goos}_${target.goarch}`, binaryName(target.goos)];
}

// .goreleaser.yaml names archives `slopcop_{{ .Tag }}_{{ .Os }}_{{ .Arch }}`;
// the leading `v` of the tag may or may not appear in the name it writes.
export function archiveNames(tag, target) {
  const version = versionFromTag(tag);
  const suffix = `${target.goos}_${target.goarch}.${target.extension}`;
  return [...new Set([`slopcop_${tag}_${suffix}`, `slopcop_${version}_${suffix}`])];
}

export function selectArchive(names, tag, target) {
  const found = archiveNames(tag, target).filter((name) => names.includes(name));
  if (found.length !== 1) {
    fail(`expected exactly one ${target.goos}/${target.goarch} archive for ${tag}, ` +
      `found ${found.length ? found.join(' and ') : 'none'} among ${names.join(', ') || 'nothing'}`);
  }
  return found[0];
}

export function parseChecksums(text) {
  const sums = new Map();
  for (const line of text.split(/\r?\n/)) {
    if (!line.trim()) continue;
    const match = /^([0-9a-fA-F]{64})\s+\*?(.+?)\s*$/.exec(line);
    if (!match) fail(`checksums.txt line ${JSON.stringify(line)} is not "<sha256>  <file>"`);
    sums.set(path.basename(match[2]), match[1].toLowerCase());
  }
  return sums;
}

export function sha256(bytes) {
  return createHash('sha256').update(bytes).digest('hex');
}

function sleep(milliseconds) {
  return new Promise((resolve) => { setTimeout(resolve, milliseconds); });
}

// `npm.cmd` cannot be spawned without a shell on Windows, so prefer the CLI
// entry point beside the running node and fall back to the PATH entry.
function npmCommand(args) {
  const cli = path.join(path.dirname(process.execPath), 'node_modules', 'npm', 'bin', 'npm-cli.js');
  if (existsSync(cli)) return { command: process.execPath, args: [cli, ...args] };
  return { command: process.platform === 'win32' ? 'npm.cmd' : 'npm', args };
}

async function npmOutput(args, cwd) {
  const { command, args: argv } = npmCommand(args);
  const { stdout } = await execFileAsync(command, argv, { cwd, maxBuffer: 64 * 1024 * 1024 });
  return stdout;
}

async function npmRun(args, cwd) {
  const { command, args: argv } = npmCommand(args);
  await new Promise((resolve, reject) => {
    const child = spawn(command, argv, { cwd, stdio: 'inherit' });
    child.on('error', reject);
    child.on('exit', (code, signal) => {
      if (code === 0) {
        resolve();
        return;
      }
      reject(new Error(`\`npm ${args[0]}\` failed with ${signal ? `signal ${signal}` : `exit code ${code}`}`));
    });
  });
}

async function runTool(command, args) {
  try {
    await execFileAsync(command, args);
    return true;
  } catch (error) {
    // A tool that is not installed is recoverable; a tool that failed is not.
    if (error.code === 'ENOENT') return false;
    fail(`\`${command}\` failed: ${String(error.stderr ?? error.message).trim()}`);
  }
}

// GNU tar cannot read zip archives and `unzip` is absent on Windows, where
// bsdtar reads both, so each format gets every tool that can unpack it.
export async function extractArchive(archive, destination) {
  await mkdir(destination, { recursive: true });
  const attempts = archive.endsWith('.zip')
    ? [['unzip', ['-q', '-o', archive, '-d', destination]], ['tar', ['-xf', archive, '-C', destination]]]
    : [['tar', ['-xzf', archive, '-C', destination]]];
  for (const [command, args] of attempts) {
    if (await runTool(command, args)) return;
  }
  fail(`could not extract ${path.basename(archive)}: ` +
    `${attempts.map(([command]) => command).join(' and ')} are both unavailable`);
}

async function stageTarget(archive, target, assets, sums, unpacked, directory) {
  const expected = sums.get(archive);
  if (!expected) fail(`${archive} has no checksums.txt entry`);
  const digest = sha256(await readFile(path.join(assets, archive)));
  if (digest !== expected) fail(`${archive} is ${digest} but checksums.txt says ${expected}`);
  const work = path.join(unpacked, archive);
  await extractArchive(path.join(assets, archive), work);
  const binary = path.join(work, binaryName(target.goos));
  if (!existsSync(binary)) fail(`${archive} does not carry ${binaryName(target.goos)}`);
  const staged = path.join(directory, ...payloadPath(target));
  await mkdir(path.dirname(staged), { recursive: true });
  await cp(binary, staged);
  await chmod(staged, 0o755);
  const { size } = await stat(staged);
  if (size === 0) fail(`${archive} carries an empty ${binaryName(target.goos)}`);
  console.log(`staged ${target.goos}/${target.goarch} from ${archive} (${size} bytes)`);
}

// Builds the package contents in `directory` and returns its manifest.
export async function stage({ assets, tag, directory }) {
  const manifest = JSON.parse(await readFile(path.join(NPM_DIRECTORY, 'package.json'), 'utf8'));
  if (manifest.name !== ROOT_PACKAGE) {
    fail(`npm/package.json names ${manifest.name}, expected ${ROOT_PACKAGE}`);
  }
  // An installation of this package runs no code of its own: the launcher and
  // the release binaries are all it carries.
  for (const field of ['scripts', 'dependencies', 'optionalDependencies', 'peerDependencies']) {
    if (manifest[field]) fail(`npm/package.json declares ${field}; the package installs no code of its own`);
  }
  const version = versionFromTag(tag);
  delete manifest.private;
  manifest.version = version;

  const names = await readdir(assets);
  const sums = parseChecksums(await readFile(path.join(assets, 'checksums.txt'), 'utf8'));

  // Unpacking happens outside the package so a failed target cannot leave a
  // half-packed directory behind for the next step to publish.
  const unpacked = await mkdtemp(path.join(os.tmpdir(), 'slopcop-npm-unpacked-'));
  try {
    await mkdir(path.join(directory, 'bin'), { recursive: true });
    const launcher = path.join(directory, 'bin', 'slopcop.cjs');
    await cp(path.join(NPM_DIRECTORY, 'bin', 'slopcop.cjs'), launcher);
    await chmod(launcher, 0o755);
    for (const name of ['README.md', 'LICENSE']) {
      await cp(path.join(REPOSITORY, name), path.join(directory, name));
    }
    for (const target of TARGETS) {
      await stageTarget(selectArchive(names, tag, target), target, assets, sums, unpacked, directory);
    }
    await writeFile(path.join(directory, 'package.json'), `${JSON.stringify(manifest, null, 2)}\n`);
    return manifest;
  } finally {
    await rm(unpacked, { recursive: true, force: true });
  }
}

// Packs the staged package and returns the tarball, its manifest, and its
// sha512 integrity, which is what the registry records for the version.
export async function pack({ assets, output, tag }) {
  const directory = await mkdtemp(path.join(os.tmpdir(), 'slopcop-npm-package-'));
  try {
    const manifest = await stage({ assets, tag, directory });
    await mkdir(output, { recursive: true });
    const reported = JSON.parse(await npmOutput(
      ['pack', '--json', '--ignore-scripts', `--pack-destination=${path.resolve(output)}`],
      directory,
    ));
    const archive = path.join(output, reported[0].filename);
    const integrity = `sha512-${createHash('sha512').update(await readFile(archive)).digest('base64')}`;
    if (reported[0].integrity !== integrity) {
      fail(`npm pack reported ${reported[0].integrity} for ${reported[0].filename}, but the tarball is ${integrity}`);
    }
    return { archive, manifest, integrity };
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
}

export async function registryVersion(name, version) {
  const url = `${REGISTRY}${name.replace('/', '%2F')}/${encodeURIComponent(version)}`;
  const response = await fetch(url, { signal: AbortSignal.timeout(30000) });
  if (response.status === 404) return null;
  if (!response.ok) fail(`the registry answered ${response.status} for ${name}@${version}`);
  return await response.json();
}

export async function publish({ archive, manifest, integrity, tag }) {
  // Automated publication is a tag action: a branch push or a manual dispatch
  // must never reach the registry.
  if (process.env.GITHUB_ACTIONS === 'true' &&
    !(process.env.GITHUB_EVENT_NAME === 'push' && process.env.GITHUB_REF === `refs/tags/${tag}`)) {
    fail('automated npm publication requires an explicit push of the release tag');
  }
  const existing = await registryVersion(manifest.name, manifest.version);
  if (existing) {
    if (existing.dist?.integrity !== integrity) {
      fail(`${manifest.name}@${manifest.version} is already published with different contents; ` +
        'reuse the version only for the exact same package');
    }
    console.log(`${manifest.name}@${manifest.version} is already published with these contents; nothing to do`);
    return;
  }
  const tagName = distTag(manifest.version);
  await npmRun(['publish', path.resolve(archive), '--access=public', '--ignore-scripts',
    `--registry=${REGISTRY}`, `--tag=${tagName}`], REPOSITORY);
  // The registry's read path lags its write path, so wait for the version to
  // become readable instead of publishing it twice.
  for (let attempt = 0; attempt < VERIFY_ATTEMPTS; attempt += 1) {
    const published = await registryVersion(manifest.name, manifest.version);
    if (published) {
      if (published.dist?.integrity !== integrity) {
        fail(`${manifest.name}@${manifest.version} is published with other contents than this run built`);
      }
      console.log(`published ${manifest.name}@${manifest.version} on the ${tagName} tag`);
      return;
    }
    if (attempt === 0) console.log('npm accepted the publish; waiting for registry metadata');
    await sleep(VERIFY_INTERVAL_MS);
  }
  fail(`${manifest.name}@${manifest.version} never became readable from the registry`);
}

function parseArguments(argv) {
  const [command, ...rest] = argv;
  if (command !== 'pack' && command !== 'publish') {
    fail(`expected \`pack\` or \`publish\`, got ${command ? `\`${command}\`` : 'no command'}`);
  }
  const options = { command, assets: 'release-assets', output: 'npm-dist', tag: process.env.RELEASE_TAG ?? '' };
  for (let index = 0; index < rest.length; index += 2) {
    const key = { '--assets': 'assets', '--output': 'output', '--release-tag': 'tag' }[rest[index]];
    if (!key) fail(`unknown flag ${rest[index]}`);
    if (rest[index + 1] === undefined) fail(`${rest[index]} needs a value`);
    options[key] = rest[index + 1];
  }
  versionFromTag(options.tag);
  return options;
}

export async function main(argv = process.argv.slice(2)) {
  const options = parseArguments(argv);
  const { archive, manifest, integrity } = await pack(options);
  console.log(`packed ${manifest.name}@${manifest.version} as ${archive}`);
  if (options.command === 'publish') await publish({ archive, manifest, integrity, tag: options.tag });
  return archive;
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch((error) => {
    console.error(`slopcop npm release: ${error.message}`);
    process.exitCode = 1;
  });
}
