// Records what the oracle's own control routes do over short workflows (#274): the
// combo, API key and model alias routes (src/app/api/combos, keys, models/alias),
// run unmodified from the pinned commit against in-memory stand-ins for its data
// layer. The routes' own rules are what is compared (required fields, the name
// pattern, duplicates, missing ids, repeating a delete); the stand-ins are plain
// create/read/update/delete, so anything the oracle's database layer adds is not
// here. internal/control/api/control_workflows_test.go replays each workflow through
// Routeweft's admin API and checks every difference against the ledger. Run:
//
//   eval "$(node compat/parity/resolve-oracle.mjs | sed 's/^/export /')"
//   node compat/parity/generate-control-workflows.mjs           # rewrite the pin
//   node compat/parity/generate-control-workflows.mjs --check   # fail if the oracle now differs
//
// No provider is called.
import assert from 'node:assert/strict';
import { readFileSync, writeFileSync } from 'node:fs';
import { loadTree, pin } from './oracle.mjs';

const response = { NextResponse: { json: (body, init) => ({ status: init?.status ?? 200, body }) } };
const copy = value => (value === undefined ? undefined : structuredClone(value));
const route = (file, stubs) => loadTree(file, { stubs: { 'next/server': response, ...stubs } });
const globals = { console: { log() {}, warn() {}, error() {} } };
void globals;

// ---- combos ------------------------------------------------------------------
async function combos() {
  const rows = []; let counter = 0;
  const db = {
    getCombos: async () => copy(rows), getComboByName: async name => copy(rows.find(row => row.name === name)), getComboById: async id => copy(rows.find(row => row.id === id)),
    createCombo: async ({ name, models, kind }) => { const row = { id: `id-${++counter}`, name, models, kind }; rows.push(row); return copy(row); },
    updateCombo: async (id, body) => { const row = rows.find(r => r.id === id); if (!row) return null; Object.assign(row, body); return copy(row); },
    deleteCombo: async id => { const index = rows.findIndex(row => row.id === id); if (index < 0) return false; rows.splice(index, 1); return true; },
  };
  const collection = await route('src/app/api/combos/route.js', { '@/lib/localDb': db });
  const item = await route('src/app/api/combos/[id]/route.js', { '@/lib/localDb': db, 'open-sse/services/combo.js': { resetComboRotation() {} } });
  const ids = {};
  const create = async (label, body) => { const r = await collection.POST({ json: async () => body }); if (r.status === 201) ids[label] = r.body.id; return r; };
  const steps = [
    ['create a combo', () => create('good', { name: 'good-name', models: [] })],
    ['create a combo with the same name', () => create('dup', { name: 'good-name', models: [] })],
    ['create a combo whose name has a space', () => create('space', { name: 'with space' })],
    ['create a combo named with dots, underscores and hyphens', () => create('dots', { name: 'dots.and_under-scores' })],
    ['create a combo with no name', () => create('none', { models: [] })],
    ['create a combo named with spaces only', () => create('blank', { name: '   ' })],
    ['rename a combo to a name in use', () => item.PUT({ json: async () => ({ name: 'good-name' }) }, { params: Promise.resolve({ id: ids.dots }) })],
    ['rename a combo to a new name', () => item.PUT({ json: async () => ({ name: 'renamed' }) }, { params: Promise.resolve({ id: ids.dots }) })],
    ['rename a combo to a name with a space', () => item.PUT({ json: async () => ({ name: 'bad name' }) }, { params: Promise.resolve({ id: ids.dots }) })],
    ['update a combo that does not exist', () => item.PUT({ json: async () => ({ name: 'ghost' }) }, { params: Promise.resolve({ id: 'missing' }) })],
    ['keep a combo\'s own name on update', () => item.PUT({ json: async () => ({ name: 'good-name' }) }, { params: Promise.resolve({ id: ids.good }) })],
    ['delete a combo', () => item.DELETE({}, { params: Promise.resolve({ id: ids.dots }) })],
    ['delete the same combo again', () => item.DELETE({}, { params: Promise.resolve({ id: ids.dots }) })],
    ['read a combo that does not exist', () => item.GET({}, { params: Promise.resolve({ id: 'missing' }) })],
  ];
  const recorded = [];
  for (const [label, run] of steps) { const r = await run(); recorded.push({ step: label, status: r.status, error: r.body?.error ?? null }); }
  return { workflow: 'combos', steps: recorded, final: { names: rows.map(row => row.name).sort() } };
}

// ---- API keys ----------------------------------------------------------------
async function keys() {
  const rows = []; let counter = 0;
  const db = {
    getApiKeys: async () => copy(rows), getApiKeyById: async id => copy(rows.find(row => row.id === id)),
    createApiKey: async (name, machineId) => { const row = { id: `key-${++counter}`, name, machineId, key: `sk-synthetic-${counter}`, isActive: true }; rows.push(row); return copy(row); },
    updateApiKey: async (id, data) => { const row = rows.find(r => r.id === id); if (!row) return null; Object.assign(row, data); return copy(row); },
    deleteApiKey: async id => { const index = rows.findIndex(row => row.id === id); if (index < 0) return false; rows.splice(index, 1); return true; },
  };
  const stubs = { '@/lib/localDb': db, '@/shared/utils/machineId': { getConsistentMachineId: async () => 'machine-1' } };
  const collection = await route('src/app/api/keys/route.js', stubs);
  const item = await route('src/app/api/keys/[id]/route.js', stubs);
  const ids = {};
  const steps = [
    ['create a key', async () => { const r = await collection.POST({ json: async () => ({ name: 'ci' }) }); ids.first = r.body.id; return r; }],
    ['create a key with no name', () => collection.POST({ json: async () => ({}) })],
    ['create a key named with spaces only', async () => { const r = await collection.POST({ json: async () => ({ name: '   ' }) }); ids.blank = r.body.id; return r; }],
    ['disable a key', () => item.PUT({ json: async () => ({ isActive: false }) }, { params: Promise.resolve({ id: ids.first }) })],
    ['update a key that does not exist', () => item.PUT({ json: async () => ({ isActive: false }) }, { params: Promise.resolve({ id: 'missing' }) })],
    ['read a key that does not exist', () => item.GET({}, { params: Promise.resolve({ id: 'missing' }) })],
    ['delete a key', () => item.DELETE({}, { params: Promise.resolve({ id: ids.first }) })],
    ['delete the same key again', () => item.DELETE({}, { params: Promise.resolve({ id: ids.first }) })],
  ];
  const recorded = [];
  for (const [label, run] of steps) { const r = await run(); recorded.push({ step: label, status: r.status, error: r.body?.error ?? null, secret: typeof r.body?.key === 'string' }); }
  return { workflow: 'keys', steps: recorded, final: { count: rows.length } };
}

// ---- model aliases -----------------------------------------------------------
async function aliases() {
  const map = {};
  const models = { getModelAliases: async () => copy(map), setModelAlias: async (alias, model) => { map[alias] = model; }, deleteModelAlias: async alias => { delete map[alias]; } };
  const handler = await route('src/app/api/models/alias/route.js', { '@/models': models });
  const get = query => new Request(`https://x.invalid/api/models/alias${query}`);
  const steps = [
    ['set an alias', () => handler.PUT({ json: async () => ({ model: 'openai/gpt-4o', alias: 'fast' }) })],
    ['set an alias with no model', () => handler.PUT({ json: async () => ({ alias: 'fast' }) })],
    ['set an alias with no name', () => handler.PUT({ json: async () => ({ model: 'openai/gpt-4o' }) })],
    ['set an alias named with spaces only', () => handler.PUT({ json: async () => ({ model: 'openai/gpt-4o', alias: '   ' }) })],
    ['point an alias at a model nothing provides', () => handler.PUT({ json: async () => ({ model: 'nowhere/none', alias: 'ghost' }) })],
    ['replace an alias', () => handler.PUT({ json: async () => ({ model: 'openai/gpt-4o', alias: 'fast' }) })],
    ['delete an alias', () => handler.DELETE(get('?alias=fast'))],
    ['delete the same alias again', () => handler.DELETE(get('?alias=fast'))],
    ['delete with no alias named', () => handler.DELETE(get(''))],
  ];
  const recorded = [];
  for (const [label, run] of steps) { const r = await run(); recorded.push({ step: label, status: r.status, error: r.body?.error ?? null }); }
  return { workflow: 'aliases', steps: recorded, final: { aliases: Object.keys(map).sort() } };
}

const workflows = [await combos(), await keys(), await aliases()];
const pinned = JSON.stringify({ oracle: 'raufimusaddiq/literouter', sha: pin, workflows }, null, 1) + '\n';
const file = new URL('../../internal/parity/testdata/control_pin.json', import.meta.url);
if (process.argv.includes('--check')) {
  const committed = JSON.parse(readFileSync(file, 'utf8'));
  assert.deepEqual({ ...JSON.parse(pinned), sha: null }, { ...committed, sha: null }, 'oracle control workflows differ from the pin; regenerate');
  console.log(`${pin}: control workflows match the pin (${workflows.reduce((n, w) => n + w.steps.length, 0)} steps)`);
} else {
  writeFileSync(file, pinned);
  console.log(`${pin}: ${workflows.length} workflows, ${workflows.reduce((n, w) => n + w.steps.length, 0)} steps`);
}
