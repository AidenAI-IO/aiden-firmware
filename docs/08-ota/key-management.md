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

Production image builds must provide a production public key. `_build_image.sh` supports two sources:

```bash
OTA_PUBLIC_KEY_PATH=/path/to/ota_pubkey.pem ./_build_image.sh
```

Or commit `keys/ota_pubkey.pem`, provided the file is not marked as dev/test/placeholder.

The build script copies the public key to the overlay and ultimately packages it into:

```text
/oem/etc/ota_pubkey.pem
```

## GitHub Secret

The CI release workflow uses a GitHub secret named `OTA_ED25519_PRIVATE_KEY`, containing the complete PEM private key:

```text
-----BEGIN PRIVATE KEY-----
...
-----END PRIVATE KEY-----
```

The workflow uses this secret to:

- Derive the corresponding public key for image builds to package into `/oem/etc/ota_pubkey.pem`.
- Sign `pico-sdk/output/image/manifest.json` after A/B images are built.

The release workflow should fail when this secret is missing.

## Local Signing

Example of generating a manifest locally:

```bash
scripts/generate_ota_manifest.sh \
  --version 20260521-120000-abcdef0 \
  --channel stable \
  --build-time 2026-05-21T12:00:00Z \
  --sign-key ota_ed25519_private_key.pem \
  --image-dir pico-sdk/output/image \
  --output pico-sdk/output/image/manifest.json
```

The script requires `boot_a.img`, `boot_b.img`, and either slot-specific or slot-neutral `oem` and `rootfs` images in `pico-sdk/output/image`. CI publishes `manifest.json` unchanged and uploads image assets as `.img.tar.gz` archives, including `update.img.tar.gz` for USB factory flashing after extraction.

## Key Rotation

V1 devices trust `/oem/etc/ota_pubkey.pem`. Do not switch the GitHub `OTA_ED25519_PRIVATE_KEY` directly before the fleet accepts the new public key, or old devices will reject manifests signed by the new private key.

Safe rotation process:

1. Generate a new Ed25519 key pair offline.
2. Keep the old private key in GitHub `OTA_ED25519_PRIVATE_KEY` temporarily.
3. Build a transition OTA containing the new `/oem/etc/ota_pubkey.pem`.
4. Sign and publish the transition release with the old private key.
5. Confirm target devices have booted and marked successful via `ota status`, release telemetry, or field inspection.
6. After confirming the fleet trusts the new public key, switch the GitHub secret to the new private key.
7. Sign subsequent releases with the new private key.

USB or factory key rotation is an alternative path:

1. Build a complete image containing the new public key.
2. Flash via USB recovery or factory process.
3. After confirming devices use the new public key, switch the GitHub signing secret for the corresponding channel.

## Private Key Compromise Handling

If the OTA private key may be compromised:

1. Delete or disable GitHub Actions secrets containing the compromised key.
2. Delete or isolate untrusted releases and assets that may have been signed with the compromised key.
3. Generate a new Ed25519 key pair offline.
4. Build a recovery image containing the new public key.
5. Prioritize USB or controlled physical recovery for devices that cannot safely trust OTA.
6. If OTA is required, publish a manually audited transition release and monitor with `ota status` and `abctl read`.
7. If repository access tokens may also be compromised, separately rotate `/userdata/ota/gh_token`. This token is only used for private repository release downloads and does not participate in manifest signing; public releases do not require token configuration. See [architecture.md](architecture.md#private-repository-token) for more runtime paths.
