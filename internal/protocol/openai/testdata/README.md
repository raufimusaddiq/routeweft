# Chat / Responses translation fixtures

Synthetic wire contracts for #75 R02 step 1. No live prompts, credentials or
provider calls. The JSON cases assert the complete translated request and
non-streaming response. Paired SSE transcripts prove incremental text,
reasoning, interleaved parallel calls, a custom-tool wrapper and a usage trailer;
the final assembled stream must match the corresponding JSON response.

Behavioral reference read-only at LiteRouter `1c3b9f47`:

- `open-sse/translator/request/openai-responses.js`
- `open-sse/translator/response/openai-responses.js`
- `open-sse/translator/formats/responsesApi.js`

These fixtures exercise Routeweft adapters and actual configured app dispatch.
They are not a differential parity certification (R11). Messages/count-tokens,
other compatibility families, provider-specific compact semantics, and opaque
server-side Responses state resolution are outside this first review boundary.
Native passthrough/unknown-field contracts remain in the existing ingress/app
tests. Translation does not create a runtime dependency on the oracle.
