// Derives the provider catalog facts Routeweft must match from the pinned
// LiteRouter source. Nothing here is a hand-written list: provider ids, auth
// modes, flows, model seeds and usage support are read out of the oracle at
// test time so a stale table can never silently drop a provider.
import assert from 'node:assert/strict';
import { pin, source, git } from './oracle.mjs';

const REGISTRY = 'open-sse/providers/registry/';

// Active provider ids, in registry order. index.js lists static imports and a
// default array; a commented-out import (`// import pNN ...`) stays hidden.
export function activeProviderIds() {
  const index = source(`${REGISTRY}index.js`);
  const imports = new Map();
  for (const match of index.matchAll(/^import (p\d+) from "\.\/(.+)\.js";/gm)) {
    imports.set(match[1], match[2]);
  }
  const body = index.split('export default [')[1];
  assert.ok(body, 'registry index has no default array');
  const active = [...body.matchAll(/^\s*(p\d+),/gm)].map(match => match[1]);
  return active.map(key => {
    const file = imports.get(key);
    assert.ok(file, `active registry entry ${key} has no import`);
    return { key, file, id: providerId(file) };
  });
}

// ProviderSecretLiterals returns every OAuth client secret the reference ships in
// its own tree. Routeweft must never republish one: this repository is public, and
// a third-party credential in public git history cannot be withdrawn. Derived from
// the oracle at test time so a new one cannot slip past a hardcoded list.
export function providerSecretLiterals() {
  const registry = git('ls-tree', '-r', '--name-only', pin)
    .split('\n')
    .filter(path => /^open-sse\/providers\/(registry\/.*|shared)\.js$/.test(path));
  const secrets = new Set();
  for (const path of registry) {
    for (const match of source(path).matchAll(/clientSecret:\s*"([^"]+)"/g)) {
      // A placeholder such as "-" is not a credential.
      if (match[1].length >= 12 && match[1] !== "-") secrets.add(match[1]);
    }
  }
  return [...secrets].sort();
}

// Providers the reference keeps hidden: imported but commented out of the default
// array. They have onboarding code but are not served, which is a product
// decision in PROVIDER_BASELINE rather than an onboarding gap.
export function hiddenProviderIds() {
  const index = source(`${REGISTRY}index.js`);
  const imports = new Map();
  for (const match of index.matchAll(/^\/\/\s*import (p\d+) from "\.\/(.+)+\.js";/gm)) {
    imports.set(match[1], match[2]);
  }
  const hidden = [];
  for (const match of index.matchAll(/^\s*\/\/\s*(p\d+),/gm)) {
    const file = imports.get(match[1]);
    if (file) hidden.push(providerId(file));
  }
  return hidden.sort();
}

function providerFile(file) {
  return `${REGISTRY}${file}.js`;
}

export function providerId(file) {
  const text = source(providerFile(file));
  const match = text.match(/^\s*id: "([^"]+)"/m);
  assert.ok(match, `${file}.js has no id`);
  return match[1];
}

// The registry's own statements about one provider: auth modes, wire formats,
// model seeds and whether it advertises usage.
export function providerFacts(id) {
  const entry = activeProviderIds().find(item => item.id === id);
  assert.ok(entry, `provider ${id} is not active in the pinned registry`);
  const text = source(providerFile(entry.file));

  const authModes = [...text.matchAll(/^\s*authModes: \[([^\]]*)\]/gm)]
    .flatMap(match => [...match[1].matchAll(/"([^"]+)"/g)].map(m => m[1]));
  const authType = text.match(/^\s*authType: "([^"]+)"/m)?.[1] ?? null;
  const category = text.match(/^\s*category: "([^"]+)"/m)?.[1] ?? null;

  const models = [];
  const block = text.split('models: [')[1]?.split('\n  ],')[0] ?? '';
  for (const match of block.matchAll(/\{\s*id: "([^"]+)"([^\n]*)\}/g)) {
    const rest = match[2];
    models.push({
      id: match[1],
      name: rest.match(/name: "([^"]*)"/)?.[1] ?? match[1],
      contextWindow: Number(rest.match(/contextLength: (\d+)/)?.[1] ?? 0),
    });
  }

  return {
    id,
    authModes: authModes.length ? authModes : authType ? [authType] : [],
    category,
    models,
    // `usage: true` is the registry's advertisement; a handler may exist without
    // it (iflow) or advertise without one (qoder), so callers compare both.
    advertisesUsage: /^\s*usage: true/m.test(text),
    advertisesUsageApiKey: /^\s*usageApikey: true/m.test(text),
  };
}

// Providers the pinned usage dispatcher can actually read. Read from the
// handler table, not from the advertisement flags.
export function usageHandledProviderIds() {
  const text = source('open-sse/services/usage.js');
  const table = text.split('const USAGE_HANDLERS = {')[1]?.split('\n};')[0];
  assert.ok(table, 'usage dispatcher has no USAGE_HANDLERS table');
  return [...table.matchAll(/^\s*"?([a-z0-9-]+)"?:/gm)].map(match => match[1]).sort();
}

// Providers with an onboarding handler, and the flow each one declares.
export function onboardingFlows() {
  const index = source('src/lib/oauth/providers/index.js');
  const imports = new Map();
  // Entries may import a default binding alongside named ones
  // (`import antigravity, { resolveAntigravityRedirectUri } from ...`).
  for (const match of index.matchAll(/^import (\w+)(?:\s*,\s*\{[^}]*\})? from "\.\/([\w-]+)\.js";/gm)) {
    imports.set(match[1], match[2]);
  }
  const block = index.split('const PROVIDERS = {')[1]?.split('\n};')[0];
  assert.ok(block, 'onboarding index has no PROVIDERS map');
  const flows = new Map();
  for (const match of block.matchAll(/^\s*(?:"([\w-]+)":\s*)?(\w+),\s*$/gm)) {
    const key = match[1] ?? match[2];
    const file = imports.get(match[2]);
    if (!file) continue;
    const text = source(`src/lib/oauth/providers/${file}.js`);
    const flowType = text.match(/flowType:\s*"([^"]+)"/)?.[1] ?? 'custom';
    flows.set(key, flowType);
  }
  // Xiaomi MiMo has no provider entry: its ECDH handshake lives in the route.
  flows.set('xiaomi-mimo', 'custom');
  return flows;
}

// Separate onboarding routes that are auth modes in their own right
// (token import, bulk import, cookie, PAT, IDE auto-import).
export function onboardingRoutes() {
  return git('ls-tree', '-r', '--name-only', pin)
    .split('\n')
    .filter(path => /^src\/app\/api\/oauth\/.*\/route\.js$/.test(path))
    .sort();
}
