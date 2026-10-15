#!/usr/bin/env node

// Builds a stand-in release directory, so the packaging can be packed and
// smoke-tested without a published release. The archives carry the names
// GoReleaser writes, and each binary answers the version check the way the
// real one does. Only POSIX can run those stand-ins: Windows can pack from
// them, but not smoke-test them.

import { execFileSync } from 'node:child_process';
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { TARGETS, binaryName, sha256, versionFromTag } from './release.mjs';

// Keep the tool's own message, which a silent stdio would throw away.
function runTool(command, args, options) {
  try {
    execFileSync(command, args, { ...options, stdio: ['ignore', 'ignore', 'pipe'] });
    return true;
  } catch (error) {
    if (error.code === 'ENOENT') return false;
    throw new Error(`${command} ${args.join(' ')} failed: ${String(error.stderr ?? error.message).trim()}`);
  }
}

export function standInBinary(tag, target) {
  return [
    '#!/bin/sh',
    'case "$1" in',
    `  version) echo "slopcop version ${tag}" ;;`,
    `  *) echo "an archive of ${tag} for ${target.goos}/${target.goarch}" ;;`,
    'esac',
    '',
  ].join('\n');
}

// Older zip builds resolve the archive path against their working directory,
// so it is always absolute here.
function zip(staging, archive) {
  const target = path.resolve(archive);
  if (runTool('zip', ['-q', '-r', target, '.'], { cwd: staging })) return;
  // bsdtar, which Windows and macOS ship, writes zip archives too.
  if (!runTool('tar', ['-a', '-cf', target, '-C', staging, '.'])) throw new Error('neither zip nor tar can write zip archives');
}

export function buildFixtureRelease(directory, tag) {
  versionFromTag(tag);
  const root = path.resolve(directory);
  mkdirSync(root, { recursive: true });
  const archives = [];
  const sums = [];
  for (const target of TARGETS) {
    const staging = mkdtempSync(path.join(os.tmpdir(), 'slopcop-fixture-target-'));
    try {
      const binary = standInBinary(tag, target);
      const binaryPath = path.join(staging, binaryName(target.goos));
      writeFileSync(binaryPath, binary, { mode: 0o755 });
      chmodSync(binaryPath, 0o755);
      writeFileSync(path.join(staging, 'README.md'), '# SlopCop\n');
      const name = `slopcop_${tag}_${target.goos}_${target.goarch}.${target.extension}`;
      const archive = path.join(root, name);
      if (target.extension === 'zip') zip(staging, archive);
      else if (!runTool('tar', ['-czf', archive, '-C', staging, '.'])) throw new Error('tar is not installed');
      archives.push({ target, name, archive, binary });
      sums.push(`${sha256(readFileSync(archive))}  ${name}`);
    } finally {
      rmSync(staging, { recursive: true, force: true });
    }
  }
  writeFileSync(path.join(root, 'checksums.txt'), `${sums.join('\n')}\n`);
  return archives;
}

function main(argv) {
  const options = { tag: 'v0.0.0-fixture', assets: 'release-assets' };
  for (let index = 0; index < argv.length; index += 2) {
    const key = { '--tag': 'tag', '--assets': 'assets' }[argv[index]];
    if (!key || argv[index + 1] === undefined) {
      throw new Error(`usage: ${path.basename(process.argv[1])} [--tag vX.Y.Z] [--assets directory]`);
    }
    options[key] = argv[index + 1];
  }
  const archives = buildFixtureRelease(options.assets, options.tag);
  console.log(`built ${archives.length} stand-in archives for ${options.tag} in ${options.assets}`);
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    main(process.argv.slice(2));
  } catch (error) {
    console.error(`slopcop npm fixture: ${error.message}`);
    process.exitCode = 1;
  }
}
