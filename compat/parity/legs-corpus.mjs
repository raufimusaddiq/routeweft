// Synthetic requests for the protocol-leg differential (#274). They are written for
// the legs, not copied from anywhere: each case isolates one thing a leg has to
// translate, so a difference names its cause. No credential, no real prompt.
const png = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==';
const fn = (name, extra = {}) => ({ type: 'function', function: { name, description: `${name} tool`, parameters: { type: 'object', properties: { path: { type: 'string' }, depth: { type: 'integer' } }, required: ['path'] }, ...extra } });
const call = (id, name, args) => ({ id, type: 'function', function: { name, arguments: JSON.stringify(args) } });

export const chat = [
  { name: 'text', body: { model: 'x', messages: [{ role: 'user', content: 'hello' }] } },
  { name: 'system and turns with sampling', body: { model: 'x', temperature: 0.3, top_p: 0.9, max_tokens: 256, stop: ['END', 'STOP'], user: 'u-1', messages: [{ role: 'system', content: 'Be terse.' }, { role: 'user', content: 'Hi' }, { role: 'assistant', content: 'Hello.' }, { role: 'user', content: 'Bye' }] } },
  { name: 'developer role', body: { model: 'x', messages: [{ role: 'developer', content: 'Follow the style guide.' }, { role: 'user', content: 'Write code' }] } },
  { name: 'system as parts', body: { model: 'x', messages: [{ role: 'system', content: [{ type: 'text', text: 'Rule one.' }, { type: 'text', text: 'Rule two.' }] }, { role: 'user', content: 'go' }] } },
  { name: 'images by url and data uri', body: { model: 'x', messages: [{ role: 'user', content: [{ type: 'text', text: 'compare' }, { type: 'image_url', image_url: { url: 'https://example.com/a.png' } }, { type: 'image_url', image_url: { url: `data:image/png;base64,${png}` } }] }] } },
  { name: 'tools with auto choice', body: { model: 'x', tools: [fn('read_file'), fn('list_dir')], tool_choice: 'auto', messages: [{ role: 'user', content: 'list it' }] } },
  { name: 'required and named tool choice', body: { model: 'x', tools: [fn('read_file')], tool_choice: { type: 'function', function: { name: 'read_file' } }, messages: [{ role: 'user', content: 'read' }] } },
  { name: 'tool choice required', body: { model: 'x', tools: [fn('read_file')], tool_choice: 'required', messages: [{ role: 'user', content: 'read' }] } },
  { name: 'tool choice none', body: { model: 'x', tools: [fn('read_file')], tool_choice: 'none', messages: [{ role: 'user', content: 'read' }] } },
  { name: 'parallel calls and results', body: { model: 'x', tools: [fn('read_file')], messages: [{ role: 'user', content: 'read both' }, { role: 'assistant', content: null, tool_calls: [call('call_1', 'read_file', { path: 'a' }), call('call_2', 'read_file', { path: 'b' })] }, { role: 'tool', tool_call_id: 'call_1', content: 'AAA' }, { role: 'tool', tool_call_id: 'call_2', content: 'BBB' }, { role: 'user', content: 'summarise' }] } },
  { name: 'assistant text with a call', body: { model: 'x', tools: [fn('read_file')], messages: [{ role: 'user', content: 'go' }, { role: 'assistant', content: 'Reading now.', tool_calls: [call('call_9', 'read_file', { path: 'z' })] }, { role: 'tool', tool_call_id: 'call_9', content: 'ZZZ' }] } },
  { name: 'tool result as parts', body: { model: 'x', tools: [fn('read_file')], messages: [{ role: 'user', content: 'go' }, { role: 'assistant', content: null, tool_calls: [call('call_3', 'read_file', { path: 'q' })] }, { role: 'tool', tool_call_id: 'call_3', content: [{ type: 'text', text: 'part one' }, { type: 'text', text: 'part two' }] }] } },
  { name: 'json object response format', body: { model: 'x', response_format: { type: 'json_object' }, messages: [{ role: 'user', content: 'json please' }] } },
  { name: 'json schema response format', body: { model: 'x', response_format: { type: 'json_schema', json_schema: { name: 'answer', schema: { type: 'object', properties: { ok: { type: 'boolean' } } } } }, messages: [{ role: 'user', content: 'json please' }] } },
  { name: 'reasoning effort', body: { model: 'x', reasoning_effort: 'high', max_completion_tokens: 4096, messages: [{ role: 'user', content: 'think' }] } },
  { name: 'thinking budget', body: { model: 'x', thinking: { type: 'enabled', budget_tokens: 2048 }, max_tokens: 8000, messages: [{ role: 'user', content: 'think' }] } },
  { name: 'cache control on content', body: { model: 'x', messages: [{ role: 'system', content: [{ type: 'text', text: 'Long stable prefix.', cache_control: { type: 'ephemeral' } }] }, { role: 'user', content: [{ type: 'text', text: 'question', cache_control: { type: 'ephemeral' } }] }] } },
  { name: 'ends on an assistant turn', body: { model: 'x', messages: [{ role: 'user', content: 'start a list' }, { role: 'assistant', content: '1.' }] } },
  { name: 'empty and null contents', body: { model: 'x', messages: [{ role: 'user', content: '' }, { role: 'assistant', content: '' }, { role: 'user', content: 'still here' }] } },
  { name: 'stream options and n', body: { model: 'x', stream: true, stream_options: { include_usage: true }, n: 1, messages: [{ role: 'user', content: 'stream' }] }, stream: true },
  { name: 'streamed with tools', body: { model: 'x', stream: true, tools: [fn('read_file')], messages: [{ role: 'user', content: 'go' }] }, stream: true },
  { name: 'named messages', body: { model: 'x', messages: [{ role: 'user', name: 'alice', content: 'hi' }, { role: 'assistant', name: 'bot', content: 'hello' }, { role: 'user', content: 'ok' }] } },
];

export const messages = [
  { name: 'text', body: { model: 'x', max_tokens: 100, messages: [{ role: 'user', content: 'hello' }] } },
  { name: 'system string and sampling', body: { model: 'x', max_tokens: 200, temperature: 0.4, top_p: 0.8, top_k: 20, stop_sequences: ['END'], system: 'Be terse.', messages: [{ role: 'user', content: 'Hi' }, { role: 'assistant', content: 'Hello.' }, { role: 'user', content: 'Bye' }] } },
  { name: 'system blocks with cache control', body: { model: 'x', max_tokens: 100, system: [{ type: 'text', text: 'Rule one.', cache_control: { type: 'ephemeral' } }, { type: 'text', text: 'Rule two.' }], messages: [{ role: 'user', content: 'go' }] } },
  { name: 'blocks and images', body: { model: 'x', max_tokens: 100, messages: [{ role: 'user', content: [{ type: 'text', text: 'look' }, { type: 'image', source: { type: 'base64', media_type: 'image/png', data: png } }, { type: 'image', source: { type: 'url', url: 'https://example.com/b.png' } }] }] } },
  { name: 'tools and choices', body: { model: 'x', max_tokens: 100, tools: [{ name: 'read_file', description: 'read', input_schema: { type: 'object', properties: { path: { type: 'string' } }, required: ['path'] } }], tool_choice: { type: 'auto' }, messages: [{ role: 'user', content: 'read' }] } },
  { name: 'any and named tool choice', body: { model: 'x', max_tokens: 100, tools: [{ name: 'read_file', input_schema: { type: 'object' } }], tool_choice: { type: 'tool', name: 'read_file' }, messages: [{ role: 'user', content: 'read' }] } },
  { name: 'tool choice any', body: { model: 'x', max_tokens: 100, tools: [{ name: 'read_file', input_schema: { type: 'object' } }], tool_choice: { type: 'any' }, messages: [{ role: 'user', content: 'read' }] } },
  { name: 'tool use and result', body: { model: 'x', max_tokens: 100, tools: [{ name: 'read_file', input_schema: { type: 'object' } }], messages: [{ role: 'user', content: 'read a' }, { role: 'assistant', content: [{ type: 'text', text: 'Reading.' }, { type: 'tool_use', id: 'toolu_1', name: 'read_file', input: { path: 'a' } }] }, { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'toolu_1', content: 'AAA' }, { type: 'text', text: 'now summarise' }] }] } },
  { name: 'tool result blocks and error', body: { model: 'x', max_tokens: 100, messages: [{ role: 'user', content: 'go' }, { role: 'assistant', content: [{ type: 'tool_use', id: 'toolu_2', name: 'run', input: {} }] }, { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'toolu_2', is_error: true, content: [{ type: 'text', text: 'failed' }, { type: 'text', text: 'badly' }] }] }] } },
  { name: 'thinking history', body: { model: 'x', max_tokens: 4000, thinking: { type: 'enabled', budget_tokens: 1024 }, messages: [{ role: 'user', content: 'go' }, { role: 'assistant', content: [{ type: 'thinking', thinking: 'hmm', signature: 'sig' }, { type: 'text', text: 'answer' }] }, { role: 'user', content: 'again' }] } },
  { name: 'metadata and stream', body: { model: 'x', max_tokens: 100, stream: true, metadata: { user_id: 'user-1' }, messages: [{ role: 'user', content: 'hello' }] }, stream: true },
  { name: 'assistant prefill', body: { model: 'x', max_tokens: 100, messages: [{ role: 'user', content: 'list' }, { role: 'assistant', content: '1.' }] } },
];

export const responses = [
  { name: 'string input', body: { model: 'x', input: 'hello' } },
  { name: 'instructions and messages', body: { model: 'x', instructions: 'Be terse.', input: [{ type: 'message', role: 'user', content: [{ type: 'input_text', text: 'Hi' }] }, { type: 'message', role: 'assistant', content: [{ type: 'output_text', text: 'Hello.' }] }, { type: 'message', role: 'user', content: [{ type: 'input_text', text: 'Bye' }] }] } },
  { name: 'role messages with strings', body: { model: 'x', input: [{ role: 'user', content: 'plain' }] } },
  { name: 'developer and system roles', body: { model: 'x', input: [{ type: 'message', role: 'developer', content: 'Follow style.' }, { type: 'message', role: 'user', content: 'go' }] } },
  { name: 'image input', body: { model: 'x', input: [{ type: 'message', role: 'user', content: [{ type: 'input_text', text: 'look' }, { type: 'input_image', image_url: `data:image/png;base64,${png}` }] }] } },
  { name: 'function tools', body: { model: 'x', tools: [{ type: 'function', name: 'read_file', description: 'read', parameters: { type: 'object', properties: { path: { type: 'string' } } } }], tool_choice: 'auto', input: 'read' } },
  { name: 'function call and output', body: { model: 'x', tools: [{ type: 'function', name: 'read_file', parameters: { type: 'object' } }], input: [{ type: 'message', role: 'user', content: 'read a' }, { type: 'function_call', call_id: 'call_1', name: 'read_file', arguments: '{"path":"a"}' }, { type: 'function_call_output', call_id: 'call_1', output: 'AAA' }] } },
  { name: 'reasoning item and effort', body: { model: 'x', reasoning: { effort: 'high' }, input: [{ type: 'message', role: 'user', content: 'think' }, { type: 'reasoning', summary: [{ type: 'summary_text', text: 'thought' }] }] } },
  { name: 'limits and format', body: { model: 'x', max_output_tokens: 300, temperature: 0.2, top_p: 0.7, text: { format: { type: 'json_schema', name: 'a', schema: { type: 'object' } } }, input: 'json' } },
  { name: 'named tool choice', body: { model: 'x', tools: [{ type: 'function', name: 'read_file', parameters: { type: 'object' } }], tool_choice: { type: 'function', name: 'read_file' }, input: 'read' } },
  { name: 'stream and store', body: { model: 'x', stream: true, store: false, input: 'hello' }, stream: true },
];

const part = text => ({ text });
export const gemini = [
  { name: 'text', body: { contents: [{ role: 'user', parts: [part('hello')] }] } },
  { name: 'system and turns', body: { systemInstruction: { parts: [part('Be terse.')] }, contents: [{ role: 'user', parts: [part('Hi')] }, { role: 'model', parts: [part('Hello.')] }, { role: 'user', parts: [part('Bye')] }] } },
  { name: 'sampling', body: { generationConfig: { temperature: 0.4, topP: 0.9, topK: 30, maxOutputTokens: 512, stopSequences: ['END'], candidateCount: 1 }, contents: [{ role: 'user', parts: [part('go')] }] } },
  { name: 'inline image', body: { contents: [{ role: 'user', parts: [part('look'), { inlineData: { mimeType: 'image/png', data: png } }] }] } },
  { name: 'function declarations', body: { tools: [{ functionDeclarations: [{ name: 'read_file', description: 'read', parameters: { type: 'object', properties: { path: { type: 'string' } } } }] }], toolConfig: { functionCallingConfig: { mode: 'AUTO' } }, contents: [{ role: 'user', parts: [part('read')] }] } },
  { name: 'function call and response', body: { tools: [{ functionDeclarations: [{ name: 'read_file', parameters: { type: 'object' } }] }], contents: [{ role: 'user', parts: [part('read a')] }, { role: 'model', parts: [{ functionCall: { name: 'read_file', args: { path: 'a' } } }] }, { role: 'user', parts: [{ functionResponse: { name: 'read_file', response: { content: 'AAA' } } }] }] } },
  { name: 'json mime type', body: { generationConfig: { responseMimeType: 'application/json' }, contents: [{ role: 'user', parts: [part('json')] }] } },
  { name: 'thinking config', body: { generationConfig: { thinkingConfig: { includeThoughts: true, thinkingBudget: 1024 } }, contents: [{ role: 'user', parts: [part('think')] }] } },
];

// ---- upstream streams for the response legs ------------------------------------
// Each is the sequence of events one provider wire sends for one answer.
const claudeStart = (usage = { input_tokens: 10, output_tokens: 1 }) => ({ type: 'message_start', message: { id: 'msg_1', type: 'message', role: 'assistant', model: 'claude-x', content: [], usage } });
const claudeText = (index, text) => [{ type: 'content_block_start', index, content_block: { type: 'text', text: '' } }, ...text.map(t => ({ type: 'content_block_delta', index, delta: { type: 'text_delta', text: t } })), { type: 'content_block_stop', index }];
const claudeTool = (index, id, name, json) => [{ type: 'content_block_start', index, content_block: { type: 'tool_use', id, name, input: {} } }, ...json.map(part => ({ type: 'content_block_delta', index, delta: { type: 'input_json_delta', partial_json: part } })), { type: 'content_block_stop', index }];
const claudeEnd = (stop, output = 7) => [{ type: 'message_delta', delta: { stop_reason: stop, stop_sequence: null }, usage: { output_tokens: output } }, { type: 'message_stop' }];
export const claudeStreams = [
  { name: 'text', events: [claudeStart(), ...claudeText(0, ['Hel', 'lo']), ...claudeEnd('end_turn')] },
  { name: 'thinking then text', events: [claudeStart(), { type: 'content_block_start', index: 0, content_block: { type: 'thinking', thinking: '' } }, { type: 'content_block_delta', index: 0, delta: { type: 'thinking_delta', thinking: 'let me ' } }, { type: 'content_block_delta', index: 0, delta: { type: 'thinking_delta', thinking: 'think' } }, { type: 'content_block_delta', index: 0, delta: { type: 'signature_delta', signature: 'sig' } }, { type: 'content_block_stop', index: 0 }, ...claudeText(1, ['Answer']), ...claudeEnd('end_turn')] },
  { name: 'text and a tool call', events: [claudeStart(), ...claudeText(0, ['Reading.']), ...claudeTool(1, 'toolu_1', 'read_file', ['{"pa', 'th":"a"}']), ...claudeEnd('tool_use')] },
  { name: 'two tool calls', events: [claudeStart(), ...claudeTool(0, 'toolu_1', 'read_file', ['{"path":"a"}']), ...claudeTool(1, 'toolu_2', 'list_dir', ['{"path":".', '"}']), ...claudeEnd('tool_use')] },
  { name: 'stopped by the token limit', events: [claudeStart(), ...claudeText(0, ['cut off']), ...claudeEnd('max_tokens', 100)] },
  { name: 'cache usage', events: [claudeStart({ input_tokens: 12, output_tokens: 1, cache_read_input_tokens: 300, cache_creation_input_tokens: 40 }), ...claudeText(0, ['ok']), ...claudeEnd('end_turn', 3)] },
  { name: 'empty tool arguments', events: [claudeStart(), ...claudeTool(0, 'toolu_1', 'now', []), ...claudeEnd('tool_use')] },
];

const chunk = (delta, finish = null, extra = {}) => ({ id: 'chatcmpl-1', object: 'chat.completion.chunk', created: 1700000000, model: 'm', choices: [{ index: 0, delta, finish_reason: finish }], ...extra });
const usageChunk = usage => ({ id: 'chatcmpl-1', object: 'chat.completion.chunk', created: 1700000000, model: 'm', choices: [], usage });
export const chatStreams = [
  { name: 'text', chunks: [chunk({ role: 'assistant', content: '' }), chunk({ content: 'Hel' }), chunk({ content: 'lo' }), chunk({}, 'stop'), usageChunk({ prompt_tokens: 10, completion_tokens: 5, total_tokens: 15 })] },
  { name: 'text without a usage chunk', chunks: [chunk({ role: 'assistant', content: 'Hi' }), chunk({}, 'stop')] },
  { name: 'reasoning then text', chunks: [chunk({ role: 'assistant', reasoning_content: 'let me ' }), chunk({ reasoning_content: 'think' }), chunk({ content: 'Answer' }), chunk({}, 'stop'), usageChunk({ prompt_tokens: 10, completion_tokens: 8, total_tokens: 18, completion_tokens_details: { reasoning_tokens: 3 } })] },
  { name: 'tool call', chunks: [chunk({ role: 'assistant', tool_calls: [{ index: 0, id: 'call_1', type: 'function', function: { name: 'read_file', arguments: '' } }] }), chunk({ tool_calls: [{ index: 0, function: { arguments: '{"pa' } }] }), chunk({ tool_calls: [{ index: 0, function: { arguments: 'th":"a"}' } }] }), chunk({}, 'tool_calls'), usageChunk({ prompt_tokens: 10, completion_tokens: 12, total_tokens: 22 })] },
  { name: 'text then a tool call', chunks: [chunk({ role: 'assistant', content: 'Reading.' }), chunk({ tool_calls: [{ index: 0, id: 'call_1', type: 'function', function: { name: 'read_file', arguments: '{"path":"a"}' } }] }), chunk({}, 'tool_calls')] },
  { name: 'two tool calls', chunks: [chunk({ role: 'assistant', tool_calls: [{ index: 0, id: 'call_1', type: 'function', function: { name: 'read_file', arguments: '{"path":"a"}' } }, { index: 1, id: 'call_2', type: 'function', function: { name: 'list_dir', arguments: '{"path":"."}' } }] }), chunk({}, 'tool_calls')] },
  { name: 'stopped by the length limit', chunks: [chunk({ role: 'assistant', content: 'cut' }), chunk({}, 'length'), usageChunk({ prompt_tokens: 10, completion_tokens: 100, total_tokens: 110 })] },
  { name: 'cached tokens', chunks: [chunk({ role: 'assistant', content: 'ok' }), chunk({}, 'stop'), usageChunk({ prompt_tokens: 340, completion_tokens: 3, total_tokens: 343, prompt_tokens_details: { cached_tokens: 300 } })] },
];

const respEvent = (type, data = {}) => ({ type, ...data });
const respMessage = (texts, extra = []) => [
  respEvent('response.created', { response: { id: 'resp_1', object: 'response', status: 'in_progress', model: 'm' } }),
  respEvent('response.output_item.added', { output_index: 0, item: { id: 'msg_1', type: 'message', role: 'assistant', content: [] } }),
  respEvent('response.content_part.added', { output_index: 0, content_index: 0, part: { type: 'output_text', text: '' } }),
  ...texts.map(delta => respEvent('response.output_text.delta', { output_index: 0, content_index: 0, item_id: 'msg_1', delta })),
  respEvent('response.output_text.done', { output_index: 0, content_index: 0, text: texts.join('') }),
  respEvent('response.output_item.done', { output_index: 0, item: { id: 'msg_1', type: 'message', role: 'assistant', content: [{ type: 'output_text', text: texts.join('') }] } }),
  ...extra,
];
const respDone = (usage = { input_tokens: 10, output_tokens: 5, total_tokens: 15 }) => respEvent('response.completed', { response: { id: 'resp_1', object: 'response', status: 'completed', model: 'm', usage } });
export const responsesStreams = [
  { name: 'text', events: [...respMessage(['Hel', 'lo']), respDone()] },
  { name: 'function call', events: [
    respEvent('response.created', { response: { id: 'resp_1', object: 'response', status: 'in_progress', model: 'm' } }),
    respEvent('response.output_item.added', { output_index: 0, item: { id: 'fc_1', type: 'function_call', call_id: 'call_1', name: 'read_file', arguments: '' } }),
    respEvent('response.function_call_arguments.delta', { output_index: 0, item_id: 'fc_1', delta: '{"pa' }),
    respEvent('response.function_call_arguments.delta', { output_index: 0, item_id: 'fc_1', delta: 'th":"a"}' }),
    respEvent('response.function_call_arguments.done', { output_index: 0, item_id: 'fc_1', arguments: '{"path":"a"}' }),
    respEvent('response.output_item.done', { output_index: 0, item: { id: 'fc_1', type: 'function_call', call_id: 'call_1', name: 'read_file', arguments: '{"path":"a"}' } }),
    respDone()] },
  { name: 'reasoning then text', events: [
    respEvent('response.created', { response: { id: 'resp_1', object: 'response', status: 'in_progress', model: 'm' } }),
    respEvent('response.output_item.added', { output_index: 0, item: { id: 'rs_1', type: 'reasoning', summary: [] } }),
    respEvent('response.reasoning_summary_part.added', { output_index: 0, item_id: 'rs_1', summary_index: 0, part: { type: 'summary_text', text: '' } }),
    respEvent('response.reasoning_summary_text.delta', { output_index: 0, item_id: 'rs_1', summary_index: 0, delta: 'let me think' }),
    respEvent('response.reasoning_summary_text.done', { output_index: 0, item_id: 'rs_1', summary_index: 0, text: 'let me think' }),
    respEvent('response.output_item.done', { output_index: 0, item: { id: 'rs_1', type: 'reasoning', summary: [{ type: 'summary_text', text: 'let me think' }] } }),
    ...respMessage(['Answer']).slice(1).map(event => ({ ...event, output_index: event.output_index === undefined ? undefined : 1 })),
    respDone({ input_tokens: 10, output_tokens: 8, total_tokens: 18, output_tokens_details: { reasoning_tokens: 3 } })] },
  { name: 'incomplete by the token limit', events: [...respMessage(['cut']), respEvent('response.incomplete', { response: { id: 'resp_1', object: 'response', status: 'incomplete', incomplete_details: { reason: 'max_output_tokens' }, model: 'm', usage: { input_tokens: 10, output_tokens: 100, total_tokens: 110 } } })] },
  { name: 'cached input tokens', events: [...respMessage(['ok']), respDone({ input_tokens: 340, output_tokens: 3, total_tokens: 343, input_tokens_details: { cached_tokens: 300 } })] },
];

const gPart = (...parts) => ({ candidates: [{ content: { role: 'model', parts }, index: 0 }] });
const gDone = (finish, usage) => ({ candidates: [{ content: { role: 'model', parts: [] }, finishReason: finish, index: 0 }], usageMetadata: usage });
export const geminiStreams = [
  { name: 'text', chunks: [gPart({ text: 'Hel' }), gPart({ text: 'lo' }), gDone('STOP', { promptTokenCount: 10, candidatesTokenCount: 5, totalTokenCount: 15 })] },
  { name: 'thought then text', chunks: [gPart({ text: 'let me think', thought: true }), gPart({ text: 'Answer' }), gDone('STOP', { promptTokenCount: 10, candidatesTokenCount: 8, thoughtsTokenCount: 3, totalTokenCount: 21 })] },
  { name: 'function call', chunks: [gPart({ functionCall: { name: 'read_file', args: { path: 'a' } } }), gDone('STOP', { promptTokenCount: 10, candidatesTokenCount: 6, totalTokenCount: 16 })] },
  { name: 'two function calls', chunks: [gPart({ functionCall: { name: 'read_file', args: { path: 'a' } } }, { functionCall: { name: 'list_dir', args: { path: '.' } } }), gDone('STOP', { promptTokenCount: 10, candidatesTokenCount: 9, totalTokenCount: 19 })] },
  { name: 'token limit', chunks: [gPart({ text: 'cut' }), gDone('MAX_TOKENS', { promptTokenCount: 10, candidatesTokenCount: 100, totalTokenCount: 110 })] },
  { name: 'blocked by safety', chunks: [gPart({ text: 'partial' }), gDone('SAFETY', { promptTokenCount: 10, candidatesTokenCount: 1, totalTokenCount: 11 })] },
  { name: 'cached content tokens', chunks: [gPart({ text: 'ok' }), gDone('STOP', { promptTokenCount: 340, candidatesTokenCount: 3, cachedContentTokenCount: 300, totalTokenCount: 343 })] },
];

export const ollamaStreams = [
  { name: 'text', lines: [{ model: 'm', message: { role: 'assistant', content: 'Hel' }, done: false }, { model: 'm', message: { role: 'assistant', content: 'lo' }, done: false }, { model: 'm', message: { role: 'assistant', content: '' }, done: true, done_reason: 'stop', prompt_eval_count: 10, eval_count: 5 }] },
  { name: 'thinking then text', lines: [{ model: 'm', message: { role: 'assistant', content: '', thinking: 'let me think' }, done: false }, { model: 'm', message: { role: 'assistant', content: 'Answer' }, done: false }, { model: 'm', message: { role: 'assistant', content: '' }, done: true, done_reason: 'stop', prompt_eval_count: 10, eval_count: 8 }] },
  { name: 'tool call', lines: [{ model: 'm', message: { role: 'assistant', content: '', tool_calls: [{ function: { name: 'read_file', arguments: { path: 'a' } } }] }, done: false }, { model: 'm', message: { role: 'assistant', content: '' }, done: true, done_reason: 'stop', prompt_eval_count: 10, eval_count: 6 }] },
  { name: 'length limit', lines: [{ model: 'm', message: { role: 'assistant', content: 'cut' }, done: false }, { model: 'm', message: { role: 'assistant', content: '' }, done: true, done_reason: 'length', prompt_eval_count: 10, eval_count: 100 }] },
  { name: 'no counts', lines: [{ model: 'm', message: { role: 'assistant', content: 'hi' }, done: false }, { model: 'm', message: { role: 'assistant', content: '' }, done: true, done_reason: 'stop' }] },
];
