# OTA Key Management

Production OTA manifests are signed with Ed25519. Devices verify signed `manifest.json` files using `/oem/etc/ota_pubkey.pem`.

## Key Generation

Generate a production Ed25519 private key offline:

```sh
openssl genpkey -algorithm ED25519 -out ota_ed25519_private_key.pem
openssl pkey -in ota_ed25519_private_key.pem -pubout -out ota_pubkey.pem
```

Keep `ota_ed25519_private_key.pem` out of git. Treat it as release-signing infrastructure.

Validate the public key before deployment:

```sh
scripts/validate_ota_pubkey.sh ota_pubkey.pem
```

The validator accepts Ed25519 public keys only. RSA, ECDSA, malformed PEM, and missing files are rejected.

## Public Key Deployment

Production image builds require a production public key. `_build_image.sh` accepts one of these sources:

- `OTA_PUBLIC_KEY_PATH=/path/to/ota_pubkey.pem ./_build_image.sh`
- `keys/ota_pubkey.pem`, if present and not marked dev/test/placeholder

The selected key is copied to `overlay/oem/etc/ota_pubkey.pem` and packaged into `/oem/etc/ota_pubkey.pem`.

CI derives the public key from the GitHub `OTA_ED25519_PRIVATE_KEY` secret before image build, validates it, and exposes it to `_build_image.sh` through `OTA_PUBLIC_KEY_PATH`.

## GitHub Secret Setup

Create a repository or environment secret named `OTA_ED25519_PRIVATE_KEY` containing the full PEM private key:

```text
-----BEGIN PRIVATE KEY-----
...
-----END PRIVATE KEY-----
```

The release workflow fails if this secret is missing. It uses the secret to:

- derive and deploy the matching public key during image build
- sign `pico-sdk/output/image/manifest.json` after the A/B images are built

The manifest version and release name use `YYYYMMDD-HHMMSS-<shortcommit>`. After signing, CI derives `/userdata/ota/config.json` from the signed manifest metadata, writes it into the SDK userdata staging directory, and repacks `userdata.img` plus `update.img` before publishing the Release.

## Manifest Signing

Local signing uses:

```sh
scripts/generate_ota_manifest.sh \
  --version 20260521-120000-abcdef0 \
  --channel stable \
  --build-time 2026-05-21T12:00:00Z \
  --sign-key ota_ed25519_private_key.pem \
  --image-dir pico-sdk/output/image \
  --output pico-sdk/output/image/manifest.json
```

The script requires `boot_a.img`, `boot_b.img`, and either slot-specific or slot-neutral `oem` and `rootfs` images. CI uploads `pico-sdk/output/image/*`, including `manifest.json` and USB `update.img`. The published `update.img` includes `/userdata/ota/config.json`, so factory-flashed devices can initialize OTA state without manual provisioning.

## Private Repository Token Behavior

Public GitHub Releases need no device token.

For private repositories, provision a least-privilege read token on the device at:

```text
/userdata/ota/gh_token
```

The token is optional and absent by default. When present, `ota` sends it as a bearer token for GitHub release metadata, manifest download, and image downloads. Keep token rotation independent of OTA signing keys.

## Key Rotation

Current V1 devices trust the Ed25519 public key deployed at `/oem/etc/ota_pubkey.pem`. Do not switch GitHub `OTA_ED25519_PRIVATE_KEY` to a new private key before the fleet has accepted an OTA that installs the matching new public key or keyring. Devices that still trust only the old public key will reject manifests signed by the new private key.

Safe OTA rotation sequence:

1. Generate the new Ed25519 key pair offline.
2. Keep GitHub `OTA_ED25519_PRIVATE_KEY` set to the old private key.
3. Build a transition OTA image that contains the new `/oem/etc/ota_pubkey.pem` or a keyring that trusts both old and new keys.
4. Publish the transition release with a manifest signed by the old private key.
5. Monitor fleet acceptance with `ota status`, release telemetry, or field checks until target devices have booted and marked the transition slot successful.
6. After the fleet trusts the new public key, update GitHub `OTA_ED25519_PRIVATE_KEY` to the new private key.
7. Publish future releases signed by the new private key.

USB or factory-only key replacement is a separate path:

1. Build a full image containing the new `/oem/etc/ota_pubkey.pem`.
2. Flash devices through USB recovery or factory provisioning.
3. After those devices boot with the new public key, update GitHub `OTA_ED25519_PRIVATE_KEY` for their release channel to the matching new private key.

If a fleet already moved to a single new public key, old-key-signed manifests are rejected unless the deployed keyring intentionally keeps the old key trusted.

## Private-Key Compromise Response

If the OTA private key may be compromised:

1. Remove or disable GitHub Actions secrets containing the compromised key.
2. Delete or quarantine untrusted releases and assets that could have been signed with it.
3. Generate a new Ed25519 key pair offline.
4. Build a recovery image containing the new public key.
5. Prefer USB or controlled physical recovery for devices that cannot safely trust OTA from the compromised key.
6. If OTA must be used, publish only a carefully audited transition release and monitor devices with `ota status` and `abctl read`.
7. Rotate any private repository `gh_token` values separately if repository access may also be compromised.

Do not commit private keys. If local development needs a throwaway key pair, pass the generated public key explicitly with `OTA_PUBLIC_KEY_PATH` and keep the private key outside the repository.
