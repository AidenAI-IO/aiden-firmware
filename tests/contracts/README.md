# Shared contract fixtures

These fixtures are deliberately language-neutral and are consumed by the C++,
Go, Node.js, and Python contract tests.

- `uds-health-request.json` is the JSON header for a health request.
- `uds-frame-response.bin` is a complete UDS frame: a 4-byte little-endian
  header length, an 8-byte little-endian payload length, JSON header, and four
  payload bytes.
- `config-wire.json` is the Config Web snake_case wire shape.
- `ota-manifest.json` is a schema-version 2 OTA manifest with deterministic
  placeholder hashes and a non-verifying Ed25519 signature placeholder. It is
  used to validate shape and canonicalization, not cryptographic trust.
