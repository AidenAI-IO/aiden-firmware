import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import vm from 'node:vm';
import {fileURLToPath} from 'node:url';

const repositoryRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const source = await fs.readFile(
  path.join(repositoryRoot, 'src/config_web/web/assets/js/llm-logs.js'),
  'utf8',
);

const element = {
  className: '',
  disabled: false,
  innerHTML: '',
  textContent: '',
};
const context = vm.createContext({
  console,
  document: {
    addEventListener() {},
    body: {appendChild() {}},
    createElement() { return {...element, click() {}, remove() {}}; },
    getElementById() { return element; },
  },
  fetch: async () => ({
    ok: true,
    status: 200,
    text: async () => JSON.stringify({files: []}),
  }),
  setTimeout,
});

vm.runInContext(source, context, {filename: 'llm-logs.js'});

function extract(body) {
  return JSON.parse(JSON.stringify(context.extractResponseMessage(body)));
}

assert.deepEqual(
  extract(JSON.stringify({
    type: 'message',
    role: 'assistant',
    content: [
      {type: 'text', text: 'Hello '},
      {type: 'tool_use', id: 'tool-1', name: 'echo', input: {value: 'test'}},
      {type: 'text', text: 'world'},
    ],
  })),
  {
    role: 'assistant',
    content: 'Hello world',
    tool_calls: [
      {
        id: 'tool-1',
        type: 'function',
        function: {name: 'echo', arguments: '{"value":"test"}'},
      },
    ],
  },
  'Anthropic JSON responses should preserve text and tool calls',
);

const anthropicSse = [
  'event: message_start',
  'data: {"type":"message_start","message":{"role":"assistant"}}',
  '',
  'event: content_block_delta',
  'data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello "}}',
  '',
  'event: content_block_delta',
  'data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"stream"}}',
  '',
  'event: content_block_start',
  'data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tool-2","name":"echo","input":{}}}',
  '',
  'event: content_block_delta',
  'data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\\"value\\":"}}',
  '',
  'event: content_block_delta',
  'data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\\"test\\"}"}}',
  '',
  'event: message_stop',
  'data: {"type":"message_stop"}',
  '',
].join('\n');

assert.deepEqual(
  extract(anthropicSse),
  {
    role: 'assistant',
    content: 'Hello stream',
    tool_calls: [
      {
        id: 'tool-2',
        type: 'function',
        function: {name: 'echo', arguments: '{"value":"test"}'},
      },
    ],
  },
  'Anthropic SSE responses should aggregate text and partial tool JSON',
);
