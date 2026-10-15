import { StringDecoder } from 'node:string_decoder';

import {
  sanitizeOpenAIResponse,
  sanitizeSseEvents,
} from './sanitize-openai-response.mjs';

function chunkToBuffer(chunk, encoding) {
  if (Buffer.isBuffer(chunk)) {
    return chunk;
  }
  if (chunk instanceof Uint8Array) {
    return Buffer.from(chunk);
  }
  return Buffer.from(String(chunk), encoding);
}

function responseIsSse(response) {
  const contentType = response.getHeader?.('content-type');
  return typeof contentType === 'string' && contentType.toLowerCase().includes('text/event-stream');
}

function looksLikeSse(text) {
  return /^\s*(?:data|event|id|retry):/.test(text);
}

function sanitizeJsonBody(body) {
  try {
    const parsed = JSON.parse(body.toString('utf8'));
    return Buffer.from(JSON.stringify(sanitizeOpenAIResponse(parsed)));
  } catch {
    // A non-JSON response (for example an upstream HTML error) must pass through.
    return body;
  }
}

function withoutContentLength(headers) {
  if (headers === null || typeof headers !== 'object') {
    return headers;
  }

  const sanitized = { ...headers };
  for (const key of Object.keys(sanitized)) {
    if (key.toLowerCase() === 'content-length') {
      delete sanitized[key];
    }
  }
  return sanitized;
}

/**
 * Express middleware for routes that proxy OpenAI-compatible responses.
 * Register it before the proxy route: `app.use(openAIResponseSanitizer())`.
 */
export function openAIResponseSanitizer() {
  return function responseSanitizerMiddleware(_request, response, next) {
    const originalWrite = response.write.bind(response);
    const originalEnd = response.end.bind(response);
    const originalWriteHead = response.writeHead?.bind(response);

    let mode = 'unknown';
    let sseRemainder = '';
    const sseDecoder = new StringDecoder('utf8');
    const jsonChunks = [];

    // Sanitization can change the byte length, so a stale upstream content-length
    // would truncate the response. Chunked transfer is safe for both JSON and SSE.
    if (originalWriteHead) {
      response.writeHead = function sanitizedWriteHead(...args) {
        response.removeHeader?.('content-length');
        const writeHeadArgs = [...args];
        if (writeHeadArgs.length >= 2) {
          writeHeadArgs[1] = withoutContentLength(writeHeadArgs[1]);
        }
        if (writeHeadArgs.length >= 3) {
          writeHeadArgs[2] = withoutContentLength(writeHeadArgs[2]);
        }
        return originalWriteHead(...writeHeadArgs);
      };
    }

    function selectMode(chunkText) {
      if (mode !== 'unknown') {
        return mode;
      }
      mode = responseIsSse(response) || looksLikeSse(chunkText) ? 'sse' : 'json';
      return mode;
    }

    function writeSseChunk(chunkText, flush = false) {
      const result = sanitizeSseEvents(sseRemainder + chunkText, flush);
      sseRemainder = result.remainder;
      if (result.output.length > 0) {
        return originalWrite(result.output);
      }
      return true;
    }

    response.write = function sanitizedWrite(chunk, encoding, callback) {
      if (chunk === undefined || chunk === null) {
        return true;
      }

      const buffer = chunkToBuffer(chunk, typeof encoding === 'string' ? encoding : undefined);
      const text = sseDecoder.write(buffer);
      if (selectMode(text) === 'sse') {
        const result = writeSseChunk(text);
        if (typeof callback === 'function') {
          callback();
        }
        return result;
      }

      jsonChunks.push(buffer);
      if (typeof callback === 'function') {
        callback();
      }
      return true;
    };

    response.end = function sanitizedEnd(chunk, encoding, callback) {
      const callbackFn = typeof encoding === 'function' ? encoding : callback;
      const encodingName = typeof encoding === 'string' ? encoding : undefined;

      if (chunk !== undefined && chunk !== null) {
        response.write(chunk, encodingName);
      }

      if (mode === 'sse') {
        writeSseChunk(sseDecoder.end(), true);
        return originalEnd(callbackFn);
      }

      if (mode === 'unknown') {
        mode = 'json';
      }

      const body = Buffer.concat(jsonChunks);
      if (body.length === 0) {
        return originalEnd(callbackFn);
      }

      response.removeHeader?.('content-length');
      return originalEnd(sanitizeJsonBody(body), callbackFn);
    };

    next();
  };
}
