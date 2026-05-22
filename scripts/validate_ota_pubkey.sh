#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then
    echo "Usage: $0 <ed25519-public-key.pem>" >&2
    exit 2
fi

key_path="$1"
if [ ! -f "$key_path" ]; then
	echo "OTA public key not found: $key_path" >&2
	exit 1
fi

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
DEV_KEY="$ROOT_DIR/keys/ota_pubkey.dev.pem"

fingerprint_key() {
	openssl pkey -pubin -in "$1" -outform DER 2>/dev/null | openssl dgst -sha256 -r | {
		read -r digest _
		printf '%s\n' "$digest"
	}
}

if ! text=$(openssl pkey -pubin -in "$key_path" -text -noout 2>/dev/null); then
	echo "OTA public key is not a valid PEM public key: $key_path" >&2
	exit 1
fi

case "$text" in
	*"ED25519 Public-Key"*) ;;
	*)
		echo "OTA public key must be Ed25519: $key_path" >&2
		exit 1
		;;
esac

if [ "${OTA_ALLOW_DEV_KEY:-}" != 1 ] && [ -f "$DEV_KEY" ]; then
	key_fingerprint=$(fingerprint_key "$key_path")
	dev_fingerprint=$(fingerprint_key "$DEV_KEY")
	if [ -n "$key_fingerprint" ] && [ "$key_fingerprint" = "$dev_fingerprint" ]; then
		echo "OTA public key matches development key material; refusing production image" >&2
		exit 1
	fi
fi

exit 0
