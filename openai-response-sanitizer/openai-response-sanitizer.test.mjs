import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

import {
  openAIResponseSanitizer,
  sanitizeOpenAIResponse,
  sanitizeSseEvents,
} from './index.mjs';

const fixturePath = (name) => fileURLToPath(new URL(`./fixtures/${name}`, import.meta.url));
const loadFixture = async (name) => JSON.parse(await readFile(fixturePath(name), 'utf8'));

function createMockResponse(headers = {}) {
  const chunks = [];
  return {
    headers: new Map(Object.entries(headers)),
    getHeader(name) {
      return this.headers.get(name);
    },
    removeHeader(name) {
      this.headers.delete(name);
    },
    writeHead() {},
    write(chunk) {
      chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(String(chunk)));
      return true;
    },
    end(chunk) {
      if (chunk !== undefined && typeof chunk !== 'function') {
        chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(String(chunk)));
      }
      this.body = Buffer.concat(chunks).toString('utf8');
    },
    body: '',
  };
}

test('case A removes internal billing/cache fields and prunes empty parents', async () => {
  const input = await loadFixture('case-a.json');
  const output = sanitizeOpenAIResponse(input);

  // The public usage counters remain available. Official nested usage details
  // (including cached_tokens) are deliberately preserved per the schema rule.
  assert.deepEqual(
    Object.keys(output.usage).filter((key) =>
      ['completion_tokens', 'prompt_tokens', 'total_tokens'].includes(key),
    ).sort(),
    [
      'completion_tokens',
      'prompt_tokens',
      'total_tokens',
    ],
  );
  assert.deepEqual(Object.keys(output.usage).sort(), [
    'cached_tokens',
    'completion_tokens',
    'completion_tokens_details',
    'prompt_tokens',
    'prompt_tokens_details',
    'total_tokens',
  ]);
  assert.deepEqual(output.usage.completion_tokens_details, {
    accepted_prediction_tokens: 0,
    audio_tokens: 0,
    cached_tokens: 0,
    reasoning_tokens: 0,
    rejected_prediction_tokens: 0,
  });
  assert.equal(output.usage.credit, undefined);
  assert.equal(output.usage.prompt_cache_hit_tokens, undefined);
  assert.equal(output.choices[0].message.content, 'ZEBRA-4291');
});

test('case B removes provider and preserves the standard response schema', async () => {
  const output = sanitizeOpenAIResponse(await loadFixture('case-b.json'));

  assert.equal(Object.hasOwn(output, 'provider'), false);
  assert.equal(output.id, 'b54ea1e8-4dad-473d-9519-e12c976339dc');
  assert.equal(output.choices[0].logprobs, null);
  assert.equal(output.usage.prompt_tokens_details.cached_tokens, 0);
});

test('case C removes provider but preserves reasoning_content and reasoning_tokens', async () => {
  const output = sanitizeOpenAIResponse(await loadFixture('case-c.json'));

  assert.equal(Object.hasOwn(output, 'provider'), false);
  assert.equal(
    output.choices[0].message.reasoning_content,
    'The user wants me to find and reply with the secret code word...',
  );
  assert.equal(output.usage.completion_tokens_details.reasoning_tokens, 45);
});

test('unknown fields remain, while nested objects made empty are removed', () => {
  const output = sanitizeOpenAIResponse({
    metadata: { request_tag: 'keep-me' },
    internal_only: { credit: 1 },
    nested: { provider: 'hidden', custom: true },
  });

  assert.deepEqual(output, {
    metadata: { request_tag: 'keep-me' },
    nested: { custom: true },
  });
});

test('SSE events are sanitized independently and [DONE] is preserved', () => {
  const source = [
    'data: {"provider":"hidden","choices":[{"message":{"content":"ok"}}]}',
    'data: [DONE]',
    '',
    '',
  ].join('\n');
  const { output, remainder } = sanitizeSseEvents(source, true);

  assert.equal(remainder, '');
  assert.match(output, /data: \{"choices":\[\{"message":\{"content":"ok"\}\}\]\}/);
  assert.match(output, /data: \[DONE\]/);
  assert.doesNotMatch(output, /provider/);
});

test('Express middleware sanitizes JSON and UTF-8 SSE chunks', () => {
  const jsonResponse = createMockResponse({ 'content-type': 'application/json' });
  openAIResponseSanitizer()({}, jsonResponse, () => {
    jsonResponse.write(JSON.stringify({ provider: 'hidden', result: { credit: 1, ok: true } }));
    jsonResponse.end();
  });
  assert.deepEqual(JSON.parse(jsonResponse.body), { result: { ok: true } });

  const sseResponse = createMockResponse({ 'content-type': 'text/event-stream' });
  const ssePayload = Buffer.from('data: {"provider":"hidden","message":"😀"}\n\n', 'utf8');
  const emojiStart = ssePayload.indexOf(Buffer.from('😀'));
  openAIResponseSanitizer()({}, sseResponse, () => {
    sseResponse.write(ssePayload.subarray(0, emojiStart + 1));
    sseResponse.write(ssePayload.subarray(emojiStart + 1));
    sseResponse.end();
  });
  assert.doesNotMatch(sseResponse.body, /provider/);
  assert.match(sseResponse.body, /😀/);
});
