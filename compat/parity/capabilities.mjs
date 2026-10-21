import vm from 'node:vm';
import { source, pin, load } from './oracle.mjs';
import { activeProviderIds } from './providers.mjs';

export async function capabilityOracle() {
  const registry = activeProviderIds().map(entry => {
    const text = source(`open-sse/providers/registry/${entry.file}.js`);
    return { id: entry.id, alias: text.match(/^\s*alias: "([^"]+)"/m)?.[1],
      aliases: [...(text.match(/^\s*aliases: \[([^\]]*)\]/m)?.[1] ?? '').matchAll(/"([^"]+)"/g)].map(m => m[1]) };
  });
  const vision = await load('open-sse/providers/visionPatterns.js', {});
  const matchPattern = (pattern, model) => new RegExp('^' + pattern.split('*').map(s => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('.*') + '$', 'i').test(model);
  const oracle = await load('open-sse/providers/capabilities.js', { REGISTRY: registry, matchPattern, ...vision });
  const code = source('open-sse/providers/capabilities.js').replace(/^import\s[\s\S]*?;\s*$/gm, '').replace(/^export\s+/gm, '');
  const data = vm.runInNewContext(`(function(){${code}; return {aliases: ALIAS_TO_PROVIDER_ID, textOnly: [...COMMANDCODE_TEXT_ONLY]};})()`, { REGISTRY: registry });
  const visionCode = source('open-sse/providers/visionPatterns.js').replace(/^export\s+/gm, '');
  const regex = vm.runInNewContext(`(function(){${visionCode}; return [NOT_VISION.source, VISION_NAME.source];})()`);
  return { oracle, data, regex, pin };
}

if (process.argv[2] === 'resolve') {
  const { readFileSync } = await import('node:fs');
  const { oracle } = await capabilityOracle();
  const cases = JSON.parse(readFileSync(0, 'utf8'));
  process.stdout.write(JSON.stringify(cases.map(({ provider, model }) => {
    const caps = oracle.getCapabilitiesForModel(provider, model);
    return { ...caps, thinkingFormat: caps.thinkingFormat ?? '',
      thinkingRange: caps.thinkingRange ?? { min: 0, max: 0 } };
  })));
}
