#!/usr/bin/env node
'use strict';

// Runs the platform binary of the release this package was packed from. The
// child owns standard input, standard output, the working directory, the
// environment, the exit status, and signals, so a run through
// `npx @brokkai/slopcop` behaves like a run of the binary itself.

const { spawn } = require('node:child_process');
const { existsSync } = require('node:fs');
const path = require('node:path');

// The GoReleaser target whose binary this package ships for each node platform.
const TARGETS = {
  'darwin-x64': 'darwin_amd64',
  'darwin-arm64': 'darwin_arm64',
  'linux-x64': 'linux_amd64',
  'linux-arm64': 'linux_arm64',
  'win32-x64': 'windows_amd64',
  'win32-arm64': 'windows_arm64',
};

const SUPPORTED = 'macOS x64/arm64, Linux x64/arm64, and Windows x64/arm64';

function targetFor(platform, arch) {
  const target = TARGETS[`${platform}-${arch}`];
  if (!target) {
    throw new Error(`unsupported platform ${platform}/${arch}; @brokkai/slopcop ships binaries for ${SUPPORTED}`);
  }
  return target;
}

// The packaged layout is native/<goos>_<goarch>/slopcop[.exe].
function binaryFor(root, platform, arch) {
  const target = targetFor(platform, arch);
  return path.join(root, 'native', target, platform === 'win32' ? 'slopcop.exe' : 'slopcop');
}

const FORWARDED_SIGNALS = ['SIGHUP', 'SIGINT', 'SIGTERM'];

function launch(binary, args, spawnProcess = spawn) {
  const child = spawnProcess(binary, args, { stdio: 'inherit' });
  const handlers = new Map();
  for (const signal of FORWARDED_SIGNALS) {
    const handler = () => child.kill(signal);
    handlers.set(signal, handler);
    process.on(signal, handler);
  }
  const release = () => {
    for (const [signal, handler] of handlers) process.removeListener(signal, handler);
  };
  child.on('error', (error) => {
    release();
    console.error(`slopcop: could not run ${binary}: ${error.message}`);
    process.exitCode = 1;
  });
  child.on('exit', (code, signal) => {
    release();
    if (signal) {
      // Die the way the binary did, so the caller sees the same signal.
      if (process.platform !== 'win32') process.kill(process.pid, signal);
      else process.exitCode = code ?? 1;
      return;
    }
    process.exitCode = code ?? 1;
  });
}

function main() {
  const root = path.resolve(__dirname, '..');
  let binary;
  try {
    binary = binaryFor(root, process.platform, process.arch);
  } catch (error) {
    console.error(`slopcop: ${error.message}`);
    process.exitCode = 1;
    return;
  }
  if (!existsSync(binary)) {
    console.error(`slopcop: ${binary} is missing from this installation; reinstall @brokkai/slopcop`);
    process.exitCode = 1;
    return;
  }
  launch(binary, process.argv.slice(2));
}

if (require.main === module) main();

module.exports = { targetFor, binaryFor, launch };
