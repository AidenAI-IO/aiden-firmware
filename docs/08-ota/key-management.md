---
sidebar_position: 2
---

# OTA Key Management

Production OTA manifests use Ed25519 signatures. Devices verify `manifest.json` through `/oem/etc/ota_pubkey.pem`, and only write to the inactive slot when both signature and image hash verification pass.

## Key Generation

Generate production Ed25519 private and public keys offline:

```bash
openssl genpkey -algorithm ed25519 -out ota_ed25519_private_key.pem
openssl pkey -in ota_ed25519_private_key.pem -pubout -out ota_pubkey.pem
```

The private key `ota_ed25519_private_key.pem` must not be committed to the repository. Treat it as release signing infrastructure.

Verify public key format:

```bash
scripts/validate_ota_pubkey.sh ota_pubkey.pem
```

The script only accepts Ed25519 public keys. RSA, ECDSA, malformed keys, or missing files will be rejected.

## Public Key Deployment

Production firmware builds must provide a matching key pair and an external
Agent configuration. Pass them to the Debian build entrypoint:

```bash
OTA_PRIVATE_KEY_PATH=/path/to/ota_ed25519_private_key.pem \
OTA_PUBLIC_KEY_PATH=/path/to/ota_pubkey.pem \
AGENT_CONFIG_PATH=/path/to/agent.toml \
./debian_build.sh
```

The build validates that both keys match and packages the public key into:

```text
/oem/etc/ota_pubkey.pem
```

## Local Signing

The complete Debian build generates and signs the local manifest:

```bash
OTA_PRIVATE_KEY_PATH=ota_ed25519_private_key.pem \
OTA_PUBLIC_KEY_PATH=ota_pubkey.pem \
AGENT_CONFIG_PATH=/path/to/agent.toml \
OTA_BUILD_VERSION=20260521-120000-abcdef0 \
OTA_CHANNEL=stable \
OTA_BUILD_TIME=2026-05-21T12:00:00Z \
./debian_build.sh
```

The build leaves `manifest.json` and the exact `.img.tar.gz` assets in
`output/debian/image`. Publication automation is outside the current scope;
publish those files without modifying or recompressing them.

## Key Rotation

V1 devices trust `/oem/etc/ota_pubkey.pem`. Do not switch the production signing private key before the fleet accepts the new public key, or old devices will reject manifests signed by it.

Safe rotation process:

1. Generate a new Ed25519 key pair offline.
2. Keep the old private key in the controlled signing environment temporarily.
3. Build a transition OTA containing the new `/oem/etc/ota_pubkey.pem`.
4. Sign and publish the transition release with the old private key.
5. Confirm target devices have booted and marked successful via `ota status`, release telemetry, or field inspection.
6. After confirming the fleet trusts the new public key, switch the signing environment to the new private key.
7. Sign subsequent releases with the new private key.

USB or factory key rotation is an alternative path:

1. Build a complete image containing the new public key.
2. Flash via USB recovery or factory process.
3. After confirming devices use the new public key, switch the signing key for the corresponding channel.

## Private Key Compromise Handling

If the OTA private key may be compromised:

1. Disable access to every signing environment containing the compromised key.
2. Delete or isolate untrusted releases and assets that may have been signed with the compromised key.
3. Generate a new Ed25519 key pair offline.
4. Build a recovery image containing the new public key.
5. Prioritize USB or controlled physical recovery for devices that cannot safely trust OTA.
6. If OTA is required, publish a manually audited transition release and monitor with `ota status` and `abctl read`.
7. If repository access tokens may also be compromised, separately rotate `/userdata/ota/gh_token`. This token is only used for private repository release downloads and does not participate in manifest signing; public releases do not require token configuration. See [architecture.md](architecture.md#private-repository-token) for more runtime paths.
