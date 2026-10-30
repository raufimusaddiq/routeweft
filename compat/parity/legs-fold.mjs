// Folds a converted stream into what a client ends up with, so two conversions of
// the same upstream answer can be compared without caring where chunk boundaries
// fall (the oracle converts one upstream chunk at a time, Routeweft converts a
// stream). internal/parity/legs_fold_test.go folds the same way in Go, and checks
// it agrees with this on the oracle's own output.
const asNumber = value => (typeof value === 'number' ? value : undefined);
const compact = object => Object.fromEntries(Object.entries(object).filter(([, value]) => value !== undefined && value !== null && value !== 0 && value !== ''));

// An OpenAI Chat chunk stream: text, reasoning, tool calls by index, finish, usage.
export function foldChat(chunks) {
  const out = { content: '', reasoning: '', toolCalls: [], finish: '', usage: {} };
  const calls = new Map();
  for (const chunk of chunks) {
    if (!chunk || typeof chunk !== 'object') continue;
    if (chunk.usage) {
      const u = chunk.usage;
      out.usage = compact({ prompt: asNumber(u.prompt_tokens), completion: asNumber(u.completion_tokens), total: asNumber(u.total_tokens), cached: asNumber(u.prompt_tokens_details?.cached_tokens ?? u.cached_tokens), reasoning: asNumber(u.completion_tokens_details?.reasoning_tokens), cacheCreation: asNumber(u.prompt_tokens_details?.cache_creation_tokens ?? u.cache_creation_input_tokens) });
    }
    const choice = chunk.choices?.[0];
    if (!choice) continue;
    const delta = choice.delta ?? choice.message ?? {};
    if (typeof delta.content === 'string') out.content += delta.content;
    if (typeof delta.reasoning_content === 'string') out.reasoning += delta.reasoning_content;
    for (const call of delta.tool_calls ?? []) {
      const index = call.index ?? calls.size;
      const entry = calls.get(index) ?? { id: '', name: '', arguments: '' };
      if (call.id) entry.id = call.id;
      if (call.function?.name) entry.name = call.function.name;
      if (typeof call.function?.arguments === 'string') entry.arguments += call.function.arguments;
      calls.set(index, entry);
    }
    if (choice.finish_reason) out.finish = choice.finish_reason;
  }
  out.toolCalls = [...calls.entries()].sort(([a], [b]) => a - b).map(([, call]) => call);
  return out;
}

// A Messages event stream: the content blocks in order, the stop reason, usage.
export function foldMessages(events) {
  const out = { blocks: [], stopReason: '', usage: {} };
  const blocks = new Map();
  const usage = {};
  for (const event of events) {
    if (!event) continue;
    if (event.type === 'message_start') Object.assign(usage, event.message?.usage ?? {});
    if (event.type === 'message_delta') { Object.assign(usage, event.usage ?? {}); if (event.delta?.stop_reason) out.stopReason = event.delta.stop_reason; }
    if (event.type === 'content_block_start') blocks.set(event.index, { type: event.content_block.type, text: '', thinking: '', id: event.content_block.id ?? '', name: event.content_block.name ?? '', json: '' });
    if (event.type === 'content_block_delta') {
      const block = blocks.get(event.index);
      if (!block) continue;
      if (event.delta.type === 'text_delta') block.text += event.delta.text;
      if (event.delta.type === 'thinking_delta') block.thinking += event.delta.thinking;
      if (event.delta.type === 'input_json_delta') block.json += event.delta.partial_json;
    }
  }
  out.blocks = [...blocks.entries()].sort(([a], [b]) => a - b).map(([, b]) => {
    if (b.type === 'text') return { type: 'text', text: b.text };
    if (b.type === 'thinking') return { type: 'thinking', text: b.thinking };
    return { type: b.type, id: b.id, name: b.name, input: b.json === '' ? {} : JSON.parse(b.json) };
  });
  out.usage = compact({ input: asNumber(usage.input_tokens), output: asNumber(usage.output_tokens), cacheRead: asNumber(usage.cache_read_input_tokens), cacheCreation: asNumber(usage.cache_creation_input_tokens) });
  return out;
}

// A Responses event stream: output items in the order they are opened, status,
// usage. Items are followed by the order they open, not by output_index: the
// oracle numbers a reasoning item and the message after it both 0.
export function foldResponses(events) {
  const out = { items: [], status: '', usage: {} };
  const items = [];
  const last = type => [...items].reverse().find(item => item.type === type);
  for (const raw of events) {
    if (!raw) continue;
    const event = raw.data ?? raw; // the Responses leg wraps each as {event, data}
    if (event.type === 'response.output_item.added') items.push({ type: event.item.type, text: '', name: event.item.name ?? '', callId: event.item.call_id ?? '', args: '', id: event.item.id });
    if (event.type === 'response.output_text.delta') { const item = last('message'); if (item) item.text += event.delta; }
    if (event.type === 'response.reasoning_summary_text.delta') { const item = last('reasoning'); if (item) item.text += event.delta; }
    if (event.type === 'response.function_call_arguments.delta') { const item = items.find(i => i.id === event.item_id) ?? last('function_call'); if (item) item.args += event.delta; }
    if (event.type === 'response.output_item.done' && event.item.type === 'function_call') {
      const item = items.find(i => i.id === event.item.id) ?? last('function_call');
      if (item) { item.name = event.item.name ?? item.name; item.callId = event.item.call_id ?? item.callId; item.args = event.item.arguments ?? item.args; }
    }
    if (['response.completed', 'response.incomplete', 'response.failed'].includes(event.type)) {
      out.status = event.response?.status ?? '';
      const u = event.response?.usage;
      if (u) out.usage = compact({ input: asNumber(u.input_tokens), output: asNumber(u.output_tokens), total: asNumber(u.total_tokens), cached: asNumber(u.input_tokens_details?.cached_tokens), reasoning: asNumber(u.output_tokens_details?.reasoning_tokens) });
      if (event.response?.incomplete_details?.reason) out.incomplete = event.response.incomplete_details.reason;
    }
  }
  out.items = items.map(i => (i.type === 'function_call' ? { type: i.type, name: i.name, callId: i.callId, arguments: i.args } : { type: i.type, text: i.text }));
  return out;
}

// A Gemini-family chunk stream (also Antigravity's, whose chunks wrap the body in
// `response`): text, thought text, function calls, finish reason, usage.
export function foldGemini(chunks) {
  const out = { text: '', thought: '', calls: [], finish: '', usage: {} };
  for (const raw of chunks) {
    const chunk = raw?.response ?? raw;
    if (!chunk) continue;
    const candidate = chunk.candidates?.[0];
    for (const part of candidate?.content?.parts ?? []) {
      if (typeof part.text === 'string') { if (part.thought) out.thought += part.text; else out.text += part.text; }
      if (part.functionCall) out.calls.push({ name: part.functionCall.name, args: part.functionCall.args ?? {} });
    }
    if (candidate?.finishReason) out.finish = candidate.finishReason;
    const u = chunk.usageMetadata;
    if (u) out.usage = compact({ prompt: asNumber(u.promptTokenCount), candidates: asNumber(u.candidatesTokenCount), thoughts: asNumber(u.thoughtsTokenCount), cached: asNumber(u.cachedContentTokenCount), total: asNumber(u.totalTokenCount) });
  }
  return out;
}
