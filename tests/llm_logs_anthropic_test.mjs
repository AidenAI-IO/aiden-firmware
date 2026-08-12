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

function requestMessages(body) {
  return JSON.parse(JSON.stringify(context.requestMessages(body)));
}

const anthropicRequest = {
  model: 'claude-sonnet-4-6',
  system: [
    {type: 'text', text: 'Use the screen carefully.'},
  ],
  messages: [
    {
      role: 'user',
      content: [
        {type: 'text', text: 'What is shown?'},
        {type: 'image', source: {type: 'base64', media_type: 'image/png', data: 'aGVsbG8='}},
      ],
    },
    {
      role: 'assistant',
      content: [
        {type: 'text', text: 'I will inspect it.'},
        {type: 'tool_use', id: 'tool-1', name: 'inspect_screen', input: {detail: 'high'}},
      ],
    },
    {
      role: 'user',
      content: [
        {type: 'tool_result', tool_use_id: 'tool-1', content: 'Screen inspected.'},
      ],
    },
  ],
};

assert.deepEqual(
  requestMessages(anthropicRequest),
  [
    {role: 'system', content: [{type: 'text', text: 'Use the screen carefully.'}]},
    {
      role: 'user',
      content: [
        {type: 'text', text: 'What is shown?'},
        {type: 'image_url', image_url: {url: 'data:image/png;base64,aGVsbG8='}},
      ],
    },
    {
      role: 'assistant',
      content: [{type: 'text', text: 'I will inspect it.'}],
      tool_calls: [
        {
          id: 'tool-1',
          type: 'function',
          function: {name: 'inspect_screen', arguments: '{"detail":"high"}'},
        },
      ],
    },
    {
      role: 'user',
      content: [
        {type: 'tool_result', tool_use_id: 'tool-1', content: 'Screen inspected.'},
      ],
    },
  ],
  'Anthropic requests should expose system, images, tool calls, and tool results to the log UI',
);

const anthropicRequestHtml = context.renderMessages(requestMessages(anthropicRequest), true, 'anthropic-request');
assert.match(anthropicRequestHtml, /Use the screen carefully\./);
assert.match(anthropicRequestHtml, /data:image\/png;base64,aGVsbG8=/);
assert.match(anthropicRequestHtml, /inspect_screen/);
assert.match(anthropicRequestHtml, /Screen inspected\./);

const openAIRequestMessages = [{role: 'assistant', content: [], tool_calls: [{id: 'call-1'}]}];
assert.deepEqual(
  requestMessages({messages: openAIRequestMessages}),
  openAIRequestMessages,
  'OpenAI request messages should retain their existing shape',
);

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

assert.deepEqual(
  extract('data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}\n'),
  {role: 'assistant', content: 'Overloaded', tool_calls: []},
  'Anthropic SSE error-only responses should render the error message',
);

const anthropicPartialErrorSse = [
  'data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Partial output"}}',
  'data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}',
  '',
].join('\n');

assert.deepEqual(
  extract(anthropicPartialErrorSse),
  {role: 'assistant', content: 'Partial output\nOverloaded', tool_calls: []},
  'Anthropic SSE errors should preserve partial output',
);
