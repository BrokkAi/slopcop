'use strict';

const assert = require('node:assert/strict');
const { spawn, spawnSync } = require('node:child_process');
const { once } = require('node:events');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { test } = require('node:test');
const { binaryFor, targetFor } = require('../bin/slopcop.cjs');

function fixture(t, withBinary = true) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'slopcop-npm-'));
  // Windows can briefly keep an exited child's files locked (EPERM/EBUSY).
  t.after(() => fs.rmSync(root, { recursive: true, force: true, maxRetries: 10, retryDelay: 100 }));
  fs.mkdirSync(path.join(root, 'bin'));
  const launcher = path.join(root, 'bin', 'slopcop.cjs');
  fs.copyFileSync(path.join(__dirname, '..', 'bin', 'slopcop.cjs'), launcher);
  if (withBinary) {
    const binary = binaryFor(root, process.platform, process.arch);
    fs.mkdirSync(path.dirname(binary), { recursive: true });
    // Node stands in for the Go binary so the test can observe arguments and IO.
    fs.copyFileSync(process.execPath, binary);
  }
  return launcher;
}

test('every packaged platform resolves and other platforms fail clearly', () => {
  for (const [platform, arch, target] of [
    ['darwin', 'x64', 'darwin_amd64'],
    ['darwin', 'arm64', 'darwin_arm64'],
    ['linux', 'x64', 'linux_amd64'],
    ['linux', 'arm64', 'linux_arm64'],
    ['win32', 'x64', 'windows_amd64'],
    ['win32', 'arm64', 'windows_arm64'],
  ]) {
    assert.equal(targetFor(platform, arch), target);
  }
  for (const [platform, arch] of [['linux', 'ia32'], ['win32', 'ia32'], ['freebsd', 'x64'], ['sunos', 'x64']]) {
    assert.throws(() => targetFor(platform, arch), /unsupported platform/);
  }
  assert.equal(binaryFor('/package', 'linux', 'arm64'), path.join('/package', 'native', 'linux_arm64', 'slopcop'));
  assert.equal(binaryFor('/package', 'win32', 'x64'), path.join('/package', 'native', 'windows_amd64', 'slopcop.exe'));
});

test('preserves arguments, cwd, environment, stdin, stdout, stderr and exit code', (t) => {
  const launcher = fixture(t);
  const program = `
    process.stdin.on('data', data => process.stdout.write(data));
    process.stdin.on('end', () => {
      process.stdout.write(JSON.stringify({ args: process.argv.slice(1), cwd: process.cwd(), env: process.env.SLOPCOP_NPM_TEST }));
      process.stderr.write('child diagnostic');
      process.exitCode = 2;
    });
  `;
  const args = ['spaces and "quotes"', '$literal; not a shell'];
  const result = spawnSync(process.execPath, [launcher, '-e', program, '--', ...args], {
    input: 'check input\n', encoding: 'utf8', env: { ...process.env, SLOPCOP_NPM_TEST: 'inherited' }, timeout: 30000,
  });
  assert.equal(result.status, 2, result.stderr);
  assert.equal(result.stdout, `check input\n${JSON.stringify({ args, cwd: process.cwd(), env: 'inherited' })}`);
  assert.equal(result.stderr, 'child diagnostic');
});

test('a missing packaged binary fails with a repair hint and no report on stdout', (t) => {
  const result = spawnSync(process.execPath, [fixture(t, false)], { encoding: 'utf8', timeout: 30000 });
  assert.equal(result.status, 1);
  assert.equal(result.stdout, '');
  assert.match(result.stderr, /is missing from this installation; reinstall @brokkai\/slopcop/);
});

test('forwards termination and keeps the child signal exit', { skip: process.platform === 'win32', timeout: 30000 }, async (t) => {
  const child = spawn(process.execPath, [fixture(t), '-e', `
    process.on('SIGTERM', () => {
      process.stderr.write('received SIGTERM');
      process.removeAllListeners('SIGTERM');
      process.kill(process.pid, 'SIGTERM');
    });
    process.stdout.write('ready');
    setInterval(() => {}, 1000);
  `]);
  t.after(() => child.kill('SIGKILL'));
  let stderr = '';
  child.stderr.on('data', (data) => { stderr += data; });
  const closed = once(child, 'close');
  await once(child.stdout, 'data');
  child.kill('SIGTERM');
  const [code, signal] = await closed;
  assert.equal(stderr, 'received SIGTERM');
  assert.equal(code, null);
  assert.equal(signal, 'SIGTERM');
});
