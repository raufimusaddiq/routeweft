// Tests for the per-run oracle resolution (#289): a moving main, a failed
// resolution and one consistent SHA throughout a run. The "remote" is a local
// repository; nothing here touches the network or LiteRouter.
import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { existsSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { resolveOracle } from './resolve-oracle.mjs';

const here = fileURLToPath(new URL('.', import.meta.url));
const git = (cwd, ...args) => execFileSync('git', ['-C', cwd, ...args], { encoding: 'utf8' }).trim();
const scratch = () => mkdtempSync(join(tmpdir(), 'resolve-oracle-test-'));

// A remote whose main can move: commit() writes marker.txt and advances main.
function remote() {
  const dir = scratch();
  git(dir, 'init', '--quiet', '--initial-branch=main');
  git(dir, 'config', 'user.email', 'test@example.invalid');
  git(dir, 'config', 'user.name', 'test');
  git(dir, 'config', 'uploadpack.allowAnySHA1InWant', 'true');
  const commit = text => {
    writeFileSync(join(dir, 'marker.txt'), text);
    git(dir, 'add', '.');
    git(dir, 'commit', '--quiet', '-m', text);
    return git(dir, 'rev-parse', 'HEAD');
  };
  return { dir, commit };
}

// Reads marker.txt the way a gate does: through oracle.mjs, under the run's env.
function gateReads(env) {
  const script = "import { pin, source } from './oracle.mjs'; process.stdout.write(JSON.stringify({ pin, marker: source('marker.txt') }));";
  const run = spawnSync(process.execPath, ['--input-type=module', '-e', script], { cwd: here, env: { PATH: process.env.PATH, ...env }, encoding: 'utf8' });
  return { status: run.status, stderr: run.stderr, out: run.status === 0 ? JSON.parse(run.stdout) : null };
}

test('main moves between runs: each run resolves the then-current commit and records it', () => {
  const origin = remote();
  const first = origin.commit('one');
  const run1 = resolveOracle({ remote: origin.dir, dir: scratch() });
  assert.equal(run1.sha, first);
  assert.equal(run1.mode, 'resolved-main');
  const second = origin.commit('two');
  const run2 = resolveOracle({ remote: origin.dir, dir: scratch() });
  assert.equal(run2.sha, second);
  assert.notEqual(run1.sha, run2.sha);
});

test('main moves during a run: the frozen run keeps reading its own commit', () => {
  const origin = remote();
  const first = origin.commit('one');
  const run = resolveOracle({ remote: origin.dir, dir: scratch() });
  origin.commit('two');
  // Every gate of the run is handed the same ORACLE_SHA/ORACLE_DIR; none re-resolves.
  const env = { ORACLE_DIR: run.dir, ORACLE_SHA: run.sha };
  const gates = [gateReads(env), gateReads(env), gateReads(env)];
  for (const gate of gates) {
    assert.equal(gate.status, 0, gate.stderr);
    assert.equal(gate.out.pin, first);
    assert.equal(gate.out.marker, 'one');
  }
});

test('a recorded SHA reproduces a run after main has moved', () => {
  const origin = remote();
  const first = origin.commit('one');
  const recorded = resolveOracle({ remote: origin.dir, dir: scratch() }).sha;
  origin.commit('two');
  const replay = resolveOracle({ remote: origin.dir, sha: recorded, dir: scratch() });
  assert.equal(replay.sha, first);
  assert.equal(replay.mode, 'recorded-sha');
  assert.equal(gateReads({ ORACLE_DIR: replay.dir, ORACLE_SHA: replay.sha }).out.marker, 'one');
});

test('a failed resolution is an error with no fallback and no record', () => {
  const missing = join(scratch(), 'nowhere');
  const dir = scratch();
  assert.throws(() => resolveOracle({ remote: missing, dir }), /resolving refs\/heads\/main .* failed; refusing to continue/);
  assert.equal(existsSync(join(dir, 'HEAD')), false, 'nothing was fetched after a failed resolve');

  // A remote that answers but has no main is a failure too, not a guess at another branch.
  const other = remote();
  other.commit('one');
  git(other.dir, 'branch', '-m', 'main', 'trunk');
  assert.throws(() => resolveOracle({ remote: other.dir, dir: scratch() }), /expected exactly one refs\/heads\/main line/);

  // An unreachable recorded SHA fails; it is not replaced by main's.
  const reachable = remote();
  reachable.commit('one');
  assert.throws(() => resolveOracle({ remote: reachable.dir, sha: 'a'.repeat(40), dir: scratch() }), /fetching a{40} .* failed/);
  assert.throws(() => resolveOracle({ remote: reachable.dir, sha: 'main', dir: scratch() }), /40-hex/);
});

test('the CLI fails with a clear message and writes no record when resolution fails', () => {
  const dir = scratch();
  const run = spawnSync(process.execPath, [join(here, 'resolve-oracle.mjs')], {
    env: { PATH: process.env.PATH, ORACLE_REMOTE: join(scratch(), 'nowhere'), ORACLE_DIR: dir, ORACLE_RECORD: join(dir, 'record.json') }, encoding: 'utf8',
  });
  assert.notEqual(run.status, 0);
  assert.match(run.stderr, /refusing to continue without a freshly resolved commit/);
  assert.equal(existsSync(join(dir, 'record.json')), false);
});

test('the CLI records the commit and the gates have no default to fall back to', () => {
  const origin = remote();
  const first = origin.commit('one');
  const dir = scratch();
  const record = join(dir, 'record.json');
  const run = spawnSync(process.execPath, [join(here, 'resolve-oracle.mjs')], {
    env: { PATH: process.env.PATH, ORACLE_REMOTE: origin.dir, ORACLE_DIR: dir, ORACLE_RECORD: record }, encoding: 'utf8',
  });
  assert.equal(run.status, 0, run.stderr);
  assert.match(run.stdout, new RegExp(`ORACLE_SHA=${first}`));
  const saved = JSON.parse(readFileSync(record, 'utf8'));
  assert.equal(saved.sha, first);
  assert.equal(saved.ref, 'refs/heads/main');
  // Without the run's SHA a gate fails; it does not pick HEAD or an old pin.
  const bare = gateReads({ ORACLE_DIR: dir });
  assert.notEqual(bare.status, 0);
  assert.match(bare.stderr, /ORACLE_SHA must be the 40-hex commit/);
});
