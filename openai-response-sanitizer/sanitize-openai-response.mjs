/**
 * Fields used by the gateway for internal routing, billing, and cache metrics.
 * Keep this list explicit: unknown future fields are preserved by default.
 */
export const INTERNAL_KEY_BLACKLIST = new Set([
  'credit',
  'provider',
  'cache_creation_input_tokens',
  'cache_read_input_tokens',
  'prompt_cache_hit_tokens',
  'prompt_cache_miss_tokens',
  'prompt_cache_write_tokens',
  'completion_thinking_tokens',
]);

const REMOVED = Symbol('removed');

function sanitizeNode(value, isRoot) {
  if (Array.isArray(value)) {
    const result = [];
    for (const item of value) {
      const sanitized = sanitizeNode(item, false);
      if (sanitized !== REMOVED) {
        result.push(sanitized);
      }
    }
    return result;
  }

  if (value !== null && typeof value === 'object') {
    const result = {};
    for (const [key, child] of Object.entries(value)) {
      if (INTERNAL_KEY_BLACKLIST.has(key)) {
        continue;
      }

      const sanitized = sanitizeNode(child, false);
      if (sanitized !== REMOVED) {
        result[key] = sanitized;
      }
    }

    // An object containing only internal fields has no useful public payload.
    if (!isRoot && Object.keys(result).length === 0) {
      return REMOVED;
    }
    return result;
  }

  return value;
}

/**
 * Clone and sanitize a parsed OpenAI-compatible response.
 * The input is never mutated. Standard and unknown fields remain untouched.
 */
export function sanitizeOpenAIResponse(value) {
  const sanitized = sanitizeNode(value, true);
  return sanitized === REMOVED ? undefined : sanitized;
}

function sanitizeSseEvent(event) {
  return event
    .split(/\r?\n/)
    .map((line) => {
      const match = line.match(/^data:\s?(.*)$/);
      if (!match || match[1] === '' || match[1] === '[DONE]') {
        return line;
      }

      try {
        const parsed = JSON.parse(match[1]);
        return `data: ${JSON.stringify(sanitizeOpenAIResponse(parsed))}`;
      } catch {
        // Preserve non-JSON data frames exactly; this also keeps provider-specific
        // SSE extensions from being corrupted by the sanitizer.
        return line;
      }
    })
    .join('\n');
}

/**
 * Sanitize one or more complete SSE events. Incomplete trailing data is returned
 * in `remainder` so a streaming middleware can wait for the next network chunk.
 */
export function sanitizeSseEvents(input, flush = false) {
  let remainder = input;
  let output = '';

  while (true) {
    const match = remainder.match(/\r?\n\r?\n/);
    if (!match) {
      break;
    }

    const event = remainder.slice(0, match.index);
    output += `${sanitizeSseEvent(event)}${match[0]}`;
    remainder = remainder.slice(match.index + match[0].length);
  }

  if (flush && remainder.length > 0) {
    output += sanitizeSseEvent(remainder);
    remainder = '';
  }

  return { output, remainder };
}
