// Reports what changed in the LiteRouter oracle between two run snapshots (#290).
//
//   node compat/parity/report-changes.mjs                       # ORACLE_DIR/ORACLE_SHA vs ORACLE_PREVIOUS
//   node compat/parity/report-changes.mjs --previous <sha|oracle-run.json>
//
// The current snapshot is the one compat/parity/resolve-oracle.mjs froze for this run
// (ORACLE_DIR, ORACLE_SHA). The previous snapshot is a recorded SHA, or an
// oracle-run.json from an earlier run. The two trees are compared by path and, where
// the oracle's own source makes it observable, by route methods, registry providers,
// translator formats/legs; each difference is linked to the Routeweft inventory
// (docs/PARITY.md, docs/PROVIDER_BASELINE.md) by the id or path it cites.
//
// Every entry is an unverified candidate. A difference is never classified as parity,
// and an unlisted surface is not a parity claim either: the report only says what
// moved upstream and what the inventory already records about it. Nothing here
// creates issues, posts comments or touches anything but the report files; the
// oracle remote is only read.
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { appendFileSync, existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { defaultRemote } from './resolve-oracle.mjs';

const sha40 = /^[0-9a-f]{40}$/;
const METHODS = /\b(?:GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\b/g;
export const disclaimer = 'Every entry is an unverified candidate. The report states what changed upstream and what the inventory records about it; it does not classify any difference as parity, and a surface it does not list is not a parity claim.';

const git = (dir, args) => execFileSync('git', ['-C', dir, ...args], { encoding: 'utf8', maxBuffer: 64 << 20, stdio: ['ignore', 'pipe', 'pipe'] });
const show = (dir, sha, path) => { try { return git(dir, ['show', `${sha}:${path}`]); } catch { return null; } };

// surfaceOf maps an oracle path to the product surface it belongs to, or null for
// files that are not product surface (tests, docs, scripts, assets).
export function surfaceOf(path) {
  if (/^src\/app\/api\/.*\/route\.(?:js|ts)$/.test(path)) return 'routes';
  if (/^src\/app\/(?:.*\/)?page\.js$/.test(path)) return 'pages';
  if (/^open-sse\/providers\/registry\/[^/]+\.js$/.test(path)) return 'providers';
  if (path === 'open-sse/providers/capabilities.js') return 'capabilities';
  if (/^open-sse\/(?:translator|handlers|executors|services|rtk|utils)\//.test(path)) return 'protocols';
  if (/^(?:open-sse\/config|open-sse\/providers|open-sse\/shared|src\/lib|src\/sse|src\/store|src\/proxy\.js)/.test(path)) return 'configuration';
  if (/^(?:src\/app|src\/shared|src\/i18n|public\/i18n)\//.test(path)) return 'ui';
  return null;
}

// Routes: the exported HTTP methods of a route file.
function routeMethods(code) {
  const methods = new Set([...code.matchAll(/export\s+(?:(?:async\s+)?function|const)\s+(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\b/g)].map(m => m[1]));
  for (const declaration of code.matchAll(/export\s*\{([^}]+)\}/g)) for (const m of declaration[1].match(METHODS) || []) methods.add(m);
  return [...methods].sort();
}

// Providers: the active imports of the registry index (a commented import stays hidden).
function activeProviders(index) {
  if (index === null) return null;
  return new Set([...index.matchAll(/^import \w+ from "\.\/(.+)\.js";/gm)].map(m => m[1]));
}

// Protocol facts: format identifiers and conversion legs, both read from source.
function formatsOf(code) { return code === null ? new Set() : new Set([...code.matchAll(/^\s+(\w+):\s*"([^"]+)"/gm)].map(m => `${m[1]}=${m[2]}`)); }
function legsOf(dir, sha, files) {
  const legs = new Set();
  for (const path of files.filter(p => /^open-sse\/translator\/(?:request|response)\/[^/]+\.js$/.test(p))) {
    const kind = path.includes('/request/') ? 'request' : 'response';
    for (const m of (show(dir, sha, path) ?? '').matchAll(/register\(\s*FORMATS\.(\w+)\s*,\s*FORMATS\.(\w+)/g)) legs.add(`${kind}:${m[1]}->${m[2]}`);
  }
  return legs;
}

const lines = text => (text ?? '').split('\n');
// linkInventory finds the lines of the Routeweft inventory that cite an id or path
// and returns where they are, their recorded status word and the issues they cite.
export function linkInventory(docs, needles) {
  const found = [];
  for (const [doc, text] of Object.entries(docs)) {
    lines(text).forEach((line, index) => {
      if (!needles.some(needle => line.includes(needle))) return;
      const status = (line.match(/\|\s*(TESTED|PARTIAL|MISSING|IMPLEMENTED|COVERED)\b/) || [])[1] ?? null;
      found.push({ doc, line: index + 1, status, issues: [...new Set(line.match(/#\d+/g) || [])] });
    });
  }
  return found.slice(0, 8);
}

function entry(surface, id, change, detail, paths, docs, needles) {
  const inventory = linkInventory(docs, needles);
  // The label says only what the comparison shows; none of them means "parity".
  const label = inventory.length === 0
    ? (change === 'removed' ? 'removed-not-inventoried' : `${change}-not-inventoried`)
    : (change === 'removed' ? 'removed-still-inventoried' : `${change}-inventoried`);
  return { surface, id, change, label, verification: 'unverified', detail, paths, inventory };
}

// buildReport compares two frozen snapshots in the oracle repository at dir.
export function buildReport({ dir, current, previous, docs = {}, now = () => new Date() }) {
  assert.match(current, sha40, 'report: current snapshot must be a 40-hex commit');
  git(dir, ['rev-parse', `${current}^{commit}`]);
  const report = { schema: 1, oracle: 'raufimusaddiq/literouter', generatedAt: now().toISOString(), status: null, current, previous: previous ?? null, disclaimer, counts: {}, entries: [], otherPaths: 0, notes: [] };
  if (previous === undefined || previous === null) {
    report.status = 'first-run';
    report.notes.push('No previous snapshot is recorded, so there is nothing to compare. This run is the baseline for the next one; no difference is reported or implied.');
    return report;
  }
  assert.match(previous, sha40, 'report: previous snapshot must be a 40-hex commit');
  try { git(dir, ['rev-parse', `${previous}^{commit}`]); } catch {
    report.status = 'previous-unavailable';
    report.notes.push(`The previous snapshot ${previous} could not be read from the oracle remote (rewritten or garbage-collected upstream). No comparison was made and no difference is reported or implied.`);
    return report;
  }
  if (previous === current) {
    report.status = 'unchanged';
    report.notes.push('The previous and current snapshots are the same commit.');
    return report;
  }
  const changed = git(dir, ['diff', '--name-status', '--no-renames', previous, current]).split('\n').filter(Boolean).map(line => {
    const [code, path] = line.split('\t');
    return { change: { A: 'added', D: 'removed', M: 'modified' }[code[0]] ?? 'modified', path };
  });
  const out = [];
  const add = (surface, id, change, detail, paths, needles) => out.push(entry(surface, id, change, detail, paths, docs, needles));

  const previousFiles = git(dir, ['ls-tree', '-r', '--name-only', previous]).split('\n');
  const currentFiles = git(dir, ['ls-tree', '-r', '--name-only', current]).split('\n');
  let other = 0;
  for (const { change, path } of changed) {
    const surface = surfaceOf(path);
    if (!surface) { other++; continue; }
    if (surface === 'routes') {
      const id = '/' + path.replace(/^src\/app\//, '').replace(/\/route\.(?:js|ts)$/, '');
      const before = change === 'added' ? [] : routeMethods(show(dir, previous, path) ?? '');
      const after = change === 'removed' ? [] : routeMethods(show(dir, current, path) ?? '');
      if (change === 'modified' && before.join() === after.join()) {
        add(surface, id, 'modified', `route source changed; exported methods unchanged (${after.join(', ') || 'none'})`, [path], [id, path]);
      } else {
        add(surface, id, change, `methods ${before.join(', ') || '-'} -> ${after.join(', ') || '-'}`, [path], [id, path]);
      }
    } else if (surface === 'providers') {
      const id = path.replace(/^open-sse\/providers\/registry\//, '').replace(/\.js$/, '');
      if (id === 'index') continue; // reported below as active/hidden changes
      add(surface, id, change, 'registry entry source changed', [path], [`\`${id}\``, `${id}.js`, path]);
    } else if (surface === 'pages') {
      add(surface, path, change, 'dashboard page source changed', [path], [path.replace(/^src\/app\/\(dashboard\)\/dashboard\//, ''), path]);
    } else {
      add(surface, path, change, 'source changed', [path], [path]);
    }
  }
  // Registry activation: an entry can stay in place and still become active or hidden.
  const before = activeProviders(show(dir, previous, 'open-sse/providers/registry/index.js'));
  const after = activeProviders(show(dir, current, 'open-sse/providers/registry/index.js'));
  if (before && after) {
    for (const id of [...after].filter(id => !before.has(id)).sort()) add('providers', id, 'activated', 'imported by the registry index, so it is an active provider', ['open-sse/providers/registry/index.js'], [`\`${id}\``]);
    for (const id of [...before].filter(id => !after.has(id)).sort()) add('providers', id, 'hidden', 'no longer imported by the registry index', ['open-sse/providers/registry/index.js'], [`\`${id}\``]);
  }
  // Protocol facts: formats and conversion legs the oracle registers.
  const formatDelta = (a, b) => [[...b].filter(x => !a.has(x)), [...a].filter(x => !b.has(x))];
  const [newFormats, goneFormats] = formatDelta(formatsOf(show(dir, previous, 'open-sse/translator/formats.js')), formatsOf(show(dir, current, 'open-sse/translator/formats.js')));
  for (const f of newFormats) add('protocols', `format ${f}`, 'added', 'format identifier added', ['open-sse/translator/formats.js'], [f.split('=')[1]]);
  for (const f of goneFormats) add('protocols', `format ${f}`, 'removed', 'format identifier removed', ['open-sse/translator/formats.js'], [f.split('=')[1]]);
  const [newLegs, goneLegs] = formatDelta(legsOf(dir, previous, previousFiles), legsOf(dir, current, currentFiles));
  for (const leg of newLegs) add('protocols', `leg ${leg}`, 'added', 'translator conversion leg registered', ['open-sse/translator'], [leg.split(':')[1]]);
  for (const leg of goneLegs) add('protocols', `leg ${leg}`, 'removed', 'translator conversion leg no longer registered', ['open-sse/translator'], [leg.split(':')[1]]);

  const order = ['routes', 'pages', 'providers', 'protocols', 'configuration', 'capabilities', 'ui'];
  report.entries = out.sort((a, b) => order.indexOf(a.surface) - order.indexOf(b.surface) || a.id.localeCompare(b.id) || a.change.localeCompare(b.change));
  report.otherPaths = other;
  report.status = report.entries.length ? 'compared' : 'compared-no-product-changes';
  for (const e of report.entries) {
    report.counts[e.surface] ??= {};
    report.counts[e.surface][e.label] = (report.counts[e.surface][e.label] ?? 0) + 1;
  }
  return report;
}

export function markdown(report) {
  const short = sha => (sha ? sha.slice(0, 12) : 'none');
  const out = [`# LiteRouter oracle change report`, '',
    `- Oracle: \`${report.oracle}\``, `- Status: **${report.status}**`,
    `- Previous snapshot: \`${report.previous ?? 'none'}\``, `- Current snapshot: \`${report.current}\``, '',
    `> ${report.disclaimer}`, ''];
  for (const note of report.notes) out.push(note, '');
  const bySurface = Map.groupBy(report.entries, e => e.surface);
  for (const [surface, entries] of bySurface) {
    out.push(`## ${surface} (${entries.length})`, '', '| Id | Change | Label | Detail | Inventory |', '| --- | --- | --- | --- | --- |');
    for (const e of entries) {
      const links = e.inventory.map(i => `${i.doc}:${i.line}${i.status ? ` ${i.status}` : ''}${i.issues.length ? ` ${i.issues.join(' ')}` : ''}`).join('<br>') || 'none';
      out.push(`| \`${e.id}\` | ${e.change} | ${e.label} | ${e.detail.replaceAll('|', '/')} | ${links} |`);
    }
    out.push('');
  }
  if (report.entries.length) out.push(`${report.otherPaths} other changed path(s) (tests, docs, scripts, assets) are not product surface and are not listed.`, '');
  out.push(`Compared ${short(report.previous)} -> ${short(report.current)} at ${report.generatedAt}.`, '');
  return out.join('\n');
}

// previousFrom accepts a 40-hex SHA or the path of an oracle-run.json record.
export function previousFrom(value) {
  if (!value) return undefined;
  if (sha40.test(value)) return value;
  assert.ok(existsSync(value), `report: --previous must be a 40-hex SHA or an oracle-run.json path, got ${JSON.stringify(value)}`);
  let record;
  try { record = JSON.parse(readFileSync(value, 'utf8')); } catch (error) { throw new Error(`report: previous record ${value} is not valid JSON: ${error.message}`); }
  assert.match(record?.sha ?? '', sha40, `report: previous record ${value} has no 40-hex sha`);
  return record.sha;
}

export function main(argv, env) {
  const flag = argv.indexOf('--previous');
  const previous = previousFrom(flag >= 0 ? argv[flag + 1] : env.ORACLE_PREVIOUS || undefined);
  const dir = env.ORACLE_DIR;
  assert.ok(dir, 'report: ORACLE_DIR must hold the run snapshot (run compat/parity/resolve-oracle.mjs)');
  assert.match(env.ORACLE_SHA ?? '', sha40, 'report: ORACLE_SHA must be the 40-hex commit of this run');
  const remote = env.ORACLE_REMOTE || defaultRemote;
  let fetchNote = null;
  if (previous !== undefined && previous !== env.ORACLE_SHA) {
    try { git(dir, ['fetch', '--quiet', '--depth=1', remote, previous]); } catch (error) { fetchNote = String(error.stderr || error.message).trim(); }
  }
  const docs = {};
  for (const doc of ['docs/PARITY.md', 'docs/PROVIDER_BASELINE.md']) if (existsSync(doc)) docs[doc] = readFileSync(doc, 'utf8');
  const report = buildReport({ dir, current: env.ORACLE_SHA, previous, docs });
  if (fetchNote) report.notes.push(`Fetching the previous snapshot failed: ${fetchNote}`);
  const outDir = env.ORACLE_REPORT_DIR || join(dir, 'change-report');
  mkdirSync(outDir, { recursive: true });
  writeFileSync(join(outDir, 'oracle-change-report.json'), `${JSON.stringify(report, null, 2)}\n`);
  const text = markdown(report);
  writeFileSync(join(outDir, 'oracle-change-report.md'), text);
  console.log(`REPORT_DIR=${outDir}\nREPORT_STATUS=${report.status}\nREPORT_ENTRIES=${report.entries.length}`);
  if (env.GITHUB_ENV) appendFileSync(env.GITHUB_ENV, `ORACLE_REPORT_DIR=${outDir}\n`);
  if (env.GITHUB_STEP_SUMMARY) appendFileSync(env.GITHUB_STEP_SUMMARY, `${text}\n`);
  return report;
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  try {
    main(process.argv.slice(2), process.env);
  } catch (error) {
    console.error(error.message);
    process.exit(1);
  }
}
