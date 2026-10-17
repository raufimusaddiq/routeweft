# Saver wire fixtures

`wire.json` fixes native outbound bodies for RTK plus lite Caveman/Ponytail.
It checks protocol placement and preservation of unknown fields, media, tool
identities, cache markers and opaque thinking data. Expected bodies are literals,
not converter-generated expectations. An unchanged second pass checks idempotence.

These synthetic Routeweft fixtures do not certify native Headroom or PXPIPE
compatibility. Their current HTTP helper contracts are exercised separately by
app and transform tests; pinned helper parity remains tracked in `docs/PARITY.md`.
