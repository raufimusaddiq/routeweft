// Tests for the oracle change report (#290): representative upstream changes, the
// first run, an unavailable previous snapshot and bad inputs. The "remote" is a local
// repository holding a miniature LiteRouter tree; nothing here touches the network.
import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { buildReport, linkInventory, main, markdown, previousFrom, surfaceOf } from './report-changes.mjs';
import { resolveOracle } from './resolve-oracle.mjs';

const here = fileURLToPath(new URL('.', import.meta.url));
const git = (cwd, ...args) => execFileSync('git', ['-C', cwd, ...args], { encoding: 'utf8' }).trim();
const scratch = () => mkdtempSync(join(tmpdir(), 'report-changes-test-'));

// A remote whose tree can change: commit({path: text | null}) writes or deletes files.
function remote() {
  const dir = scratch();
  git(dir, 'init', '--quiet', '--initial-branch=main');
  git(dir, 'config', 'user.email', 'test@example.invalid');
  git(dir, 'config', 'user.name', 'test');
  git(dir, 'config', 'uploadpack.allowAnySHA1InWant', 'true');
  let n = 0;
  const commit = files => {
    for (const [path, text] of Object.entries(files)) {
      if (text === null) { rmSync(join(dir, path)); continue; }
      mkdirSync(dirname(join(dir, path)), { recursive: true });
      writeFileSync(join(dir, path), text);
    }
    git(dir, 'add', '-A');
    git(dir, 'commit', '--quiet', '-m', `c${++n}`);
    return git(dir, 'rev-parse', 'HEAD');
  };
  return { dir, commit };
}

const docs = {
  'docs/PARITY.md': [
    '| 3 | `/api/keys` | GET, POST | K | shape | PARTIAL | /api/keys | R11: product decision: tracked in #77 |',
    '| `openai` | API key | C | seed | 3/0/3 | P | TESTED | covered by #119 |',
    '| `legacy` | API key | C | seed | 1/0/1 | P | PARTIAL | #76 |',
  ].join('\n'),
};

const base = {
  'src/app/api/keys/route.js': 'export async function GET() {}\n',
  'src/app/api/old/route.js': 'export async function GET() {}\n',
  'src/app/api/same/route.js': 'export async function GET() {}\n',
  'src/app/(dashboard)/dashboard/usage/page.js': 'export default 1;\n',
  'open-sse/providers/registry/index.js': 'import p1 from "./openai.js";\nimport p2 from "./legacy.js";\n// import p3 from "./hidden.js";\nexport default [\n  p1,\n  p2,\n];\n',
  'open-sse/providers/registry/openai.js': 'export default { id: "openai" };\n',
  'open-sse/providers/registry/legacy.js': 'export default { id: "legacy" };\n',
  'open-sse/providers/registry/hidden.js': 'export default { id: "hidden" };\n',
  'open-sse/translator/formats.js': 'export const FORMATS = {\n  OPENAI: "openai",\n  CLAUDE: "claude",\n};\n',
  'open-sse/translator/request/claude-to-openai.js': 'register(FORMATS.CLAUDE, FORMATS.OPENAI, fn, null);\n',
  'open-sse/config/settings.js': 'export const a = 1;\n',
  'open-sse/providers/capabilities.js': 'export const caps = {};\n',
  'tests/unit/x.test.js': 'one\n',
};

// Everything below changes against `base`.
const next = {
  'src/app/api/keys/route.js': 'export async function GET() {}\nexport async function DELETE() {}\n', // method added
  'src/app/api/old/route.js': null, // route removed
  'src/app/api/webhooks/route.js': 'export async function POST() {}\n', // route added, not inventoried
  'src/app/(dashboard)/dashboard/audit/page.js': 'export default 2;\n', // page added
  'open-sse/providers/registry/index.js': 'import p1 from "./openai.js";\nimport p3 from "./hidden.js";\nexport default [\n  p1,\n  p3,\n];\n', // legacy hidden, hidden activated
  'open-sse/providers/registry/openai.js': 'export default { id: "openai", models: 4 };\n',
  'open-sse/providers/registry/newcomer.js': 'export default { id: "newcomer" };\n',
  'open-sse/translator/formats.js': 'export const FORMATS = {\n  OPENAI: "openai",\n  CLAUDE: "claude",\n  MISTRAL: "mistral",\n};\n',
  'open-sse/translator/request/mistral-to-openai.js': 'register(FORMATS.MISTRAL, FORMATS.OPENAI, fn, null);\n',
  'open-sse/config/settings.js': 'export const a = 2;\n',
  'open-sse/providers/capabilities.js': 'export const caps = { a: 1 };\n',
  'tests/unit/x.test.js': 'two\n',
};

function snapshots(changes = next) {
  const origin = remote();
  const previous = origin.commit(base);
  const current = origin.commit(changes);
  const run = resolveOracle({ remote: origin.dir, sha: current, dir: scratch() });
  git(run.dir, 'fetch', '--quiet', '--depth=1', origin.dir, previous); // what the CLI does for the previous snapshot
  return { origin, previous, current, dir: run.dir };
}

const find = (report, surface, id, change) => report.entries.find(e => e.surface === surface && e.id === id && (!change || e.change === change));

test('representative upstream changes are reported per surface with both SHAs and inventory links', () => {
  const { dir, previous, current } = snapshots();
  const report = buildReport({ dir, previous, current, docs });
  assert.equal(report.status, 'compared');
  assert.equal(report.previous, previous);
  assert.equal(report.current, current);

  assert.match(find(report, 'routes', '/api/keys').detail, /GET -> DELETE, GET/);
  assert.equal(find(report, 'routes', '/api/keys').label, 'modified-inventoried');
  assert.equal(find(report, 'routes', '/api/keys').inventory[0].status, 'PARTIAL');
  assert.deepEqual(find(report, 'routes', '/api/keys').inventory[0].issues, ['#77']);
  assert.equal(find(report, 'routes', '/api/old').label, 'removed-not-inventoried');
  assert.equal(find(report, 'routes', '/api/webhooks').label, 'added-not-inventoried');
  assert.equal(find(report, 'routes', '/api/same'), undefined, 'an unchanged route is not reported');

  assert.equal(find(report, 'pages', 'src/app/(dashboard)/dashboard/audit/page.js').change, 'added');

  assert.equal(find(report, 'providers', 'newcomer').label, 'added-not-inventoried');
  assert.equal(find(report, 'providers', 'openai').label, 'modified-inventoried');
  assert.equal(find(report, 'providers', 'openai').inventory[0].status, 'TESTED');
  assert.equal(find(report, 'providers', 'hidden', 'activated').change, 'activated');
  assert.equal(find(report, 'providers', 'legacy', 'hidden').label, 'hidden-inventoried');
  assert.deepEqual(find(report, 'providers', 'legacy', 'hidden').inventory[0].issues, ['#76']);

  assert.ok(find(report, 'protocols', 'format MISTRAL=mistral'));
  assert.ok(find(report, 'protocols', 'leg request:MISTRAL->OPENAI'));
  assert.equal(find(report, 'protocols', 'leg request:CLAUDE->OPENAI'), undefined, 'an unchanged leg is not reported');
  assert.ok(find(report, 'configuration', 'open-sse/config/settings.js'));
  assert.ok(find(report, 'capabilities', 'open-sse/providers/capabilities.js'));
  assert.equal(report.otherPaths, 1, 'tests are counted, not listed');
});

test('no entry is ever classified as parity or verified, and the report says so', () => {
  const { dir, previous, current } = snapshots();
  const report = buildReport({ dir, previous, current, docs });
  assert.ok(report.entries.length > 5);
  for (const e of report.entries) {
    assert.equal(e.verification, 'unverified');
    assert.doesNotMatch(`${e.label} ${e.change}`, /parity|\bverified|\bmatch|\bequal/);
  }
  assert.match(report.disclaimer, /does not classify any difference as parity/);
  assert.match(markdown(report), /unverified candidate/);
});

test('the first run has nothing to compare and says so without inventing a difference', () => {
  const { dir, current } = snapshots();
  const report = buildReport({ dir, previous: undefined, current, docs });
  assert.equal(report.status, 'first-run');
  assert.deepEqual(report.entries, []);
  assert.match(report.notes[0], /No previous snapshot is recorded/);
  assert.match(markdown(report), /first-run/);
});

test('a previous snapshot the remote no longer has is unavailable, not an empty diff', () => {
  const { dir, current } = snapshots();
  const report = buildReport({ dir, previous: 'a'.repeat(40), current, docs });
  assert.equal(report.status, 'previous-unavailable');
  assert.deepEqual(report.entries, []);
  assert.match(report.notes[0], /could not be read/);
});

test('the same commit twice is unchanged, and a change outside product surface reports none', () => {
  const { dir, current } = snapshots();
  assert.equal(buildReport({ dir, previous: current, current, docs }).status, 'unchanged');

  const origin = remote();
  const previous = origin.commit(base);
  const only = origin.commit({ 'tests/unit/x.test.js': 'three\n', 'README.md': 'docs\n' });
  const run = resolveOracle({ remote: origin.dir, sha: only, dir: scratch() });
  git(run.dir, 'fetch', '--quiet', '--depth=1', origin.dir, previous);
  const report = buildReport({ dir: run.dir, previous, current: only, docs });
  assert.equal(report.status, 'compared-no-product-changes');
  assert.equal(report.otherPaths, 2);
});

test('invalid inputs fail with a clear message', () => {
  const { dir, current } = snapshots();
  assert.throws(() => buildReport({ dir, previous: 'main', current, docs }), /previous snapshot must be a 40-hex/);
  assert.throws(() => buildReport({ dir, previous: current, current: 'main', docs }), /current snapshot must be a 40-hex/);
  assert.throws(() => buildReport({ dir, previous: current, current: 'b'.repeat(40), docs }));
  assert.throws(() => previousFrom(join(scratch(), 'missing.json')), /40-hex SHA or an oracle-run.json path/);
  const bad = join(scratch(), 'record.json');
  writeFileSync(bad, '{"sha":"nope"}');
  assert.throws(() => previousFrom(bad), /has no 40-hex sha/);
  writeFileSync(bad, 'not json');
  assert.throws(() => previousFrom(bad), /not valid JSON/);
  assert.equal(previousFrom(undefined), undefined);
});

test('surfaces and inventory links are derived from paths and cited ids only', () => {
  assert.equal(surfaceOf('src/app/api/a/b/route.js'), 'routes');
  assert.equal(surfaceOf('tests/unit/a.test.js'), null);
  assert.equal(surfaceOf('docs/x.md'), null);
  assert.deepEqual(linkInventory({ d: 'a `x` b #4 #4 | PARTIAL' }, ['`y`']), []);
  assert.deepEqual(linkInventory({ d: '| `x` | PARTIAL | #4 #4' }, ['`x`']), [{ doc: 'd', line: 1, status: 'PARTIAL', issues: ['#4'] }]);
});

test('the CLI writes the JSON and Markdown report from a recorded previous run and posts nothing', () => {
  const origin = remote();
  const previous = origin.commit(base);
  const current = origin.commit(next);
  const dir = resolveOracle({ remote: origin.dir, sha: current, dir: scratch() }).dir;
  const record = join(scratch(), 'oracle-run.json');
  writeFileSync(record, JSON.stringify({ sha: previous }));
  const reportDir = scratch();
  const summary = join(scratch(), 'summary.md');
  const env = { PATH: process.env.PATH, ORACLE_DIR: dir, ORACLE_SHA: current, ORACLE_REMOTE: origin.dir, ORACLE_REPORT_DIR: reportDir, ORACLE_PREVIOUS: record, GITHUB_STEP_SUMMARY: summary };
  const run = spawnSync(process.execPath, [join(here, 'report-changes.mjs')], { env, encoding: 'utf8', cwd: join(here, '..', '..') });
  assert.equal(run.status, 0, run.stderr);
  assert.match(run.stdout, /REPORT_STATUS=compared/);
  const saved = JSON.parse(readFileSync(join(reportDir, 'oracle-change-report.json'), 'utf8'));
  assert.equal(saved.previous, previous);
  assert.equal(saved.current, current);
  assert.ok(saved.entries.some(e => e.id === '/api/webhooks'));
  assert.match(readFileSync(join(reportDir, 'oracle-change-report.md'), 'utf8'), /\/api\/webhooks/);
  assert.match(readFileSync(summary, 'utf8'), /LiteRouter oracle change report/);
});

test('the CLI reports the first run, an unreachable previous and bad input explicitly', () => {
  const origin = remote();
  origin.commit(base);
  const current = origin.commit(next);
  const dir = resolveOracle({ remote: origin.dir, sha: current, dir: scratch() }).dir;
  const run = (extra, reportDir = scratch()) => ({
    reportDir,
    ...spawnSync(process.execPath, [join(here, 'report-changes.mjs')], { env: { PATH: process.env.PATH, ORACLE_DIR: dir, ORACLE_SHA: current, ORACLE_REMOTE: origin.dir, ORACLE_REPORT_DIR: reportDir, ...extra }, encoding: 'utf8', cwd: join(here, '..', '..') }),
  });
  const first = run({});
  assert.equal(first.status, 0, first.stderr);
  assert.match(first.stdout, /REPORT_STATUS=first-run/);

  const gone = run({ ORACLE_PREVIOUS: 'c'.repeat(40) });
  assert.equal(gone.status, 0, gone.stderr);
  assert.match(gone.stdout, /REPORT_STATUS=previous-unavailable/);
  const saved = JSON.parse(readFileSync(join(gone.reportDir, 'oracle-change-report.json'), 'utf8'));
  assert.ok(saved.notes.some(n => /Fetching the previous snapshot failed/.test(n)));

  const bad = run({ ORACLE_PREVIOUS: 'not-a-sha' });
  assert.notEqual(bad.status, 0);
  assert.match(bad.stderr, /40-hex SHA or an oracle-run.json path/);
  assert.equal(existsSync(join(bad.reportDir, 'oracle-change-report.json')), false);

  const noRun = spawnSync(process.execPath, [join(here, 'report-changes.mjs')], { env: { PATH: process.env.PATH }, encoding: 'utf8', cwd: join(here, '..', '..') });
  assert.notEqual(noRun.status, 0);
  assert.match(noRun.stderr, /ORACLE_DIR must hold the run snapshot/);
});

test('main() in-process matches the CLI contract', () => {
  const origin = remote();
  const previous = origin.commit(base);
  const current = origin.commit(next);
  const dir = resolveOracle({ remote: origin.dir, sha: current, dir: scratch() }).dir;
  const report = main(['--previous', previous], { ORACLE_DIR: dir, ORACLE_SHA: current, ORACLE_REMOTE: origin.dir, ORACLE_REPORT_DIR: scratch() });
  assert.equal(report.status, 'compared');
});
