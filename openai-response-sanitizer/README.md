# OpenAI response sanitizer

Zero-dependency Node.js utilities for stripping gateway-only fields before an OpenAI-compatible response reaches a client.

## Express

```js
import express from 'express';
import { openAIResponseSanitizer } from './index.mjs';

const app = express();
app.use(openAIResponseSanitizer());
// Register the upstream proxy route after the middleware.
```

The middleware handles regular JSON responses and `text/event-stream` responses. It keeps unknown fields, standard OpenAI fields, reasoning content, non-JSON SSE frames, and `data: [DONE]` intact.

## Pure function

```js
import { sanitizeOpenAIResponse } from './index.mjs';

const publicResponse = sanitizeOpenAIResponse(upstreamResponse);
```

Run the fixture tests with:

```sh
node --test openai-response-sanitizer/openai-response-sanitizer.test.mjs
```
