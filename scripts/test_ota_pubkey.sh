#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
VALIDATOR="$ROOT_DIR/scripts/validate_ota_pubkey.sh"

if [ ! -x "$VALIDATOR" ]; then
    echo "missing executable $VALIDATOR" >&2
    exit 1
fi

TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT INT TERM

openssl genpkey -algorithm Ed25519 -out "$TMP_DIR/ed25519.key" >/dev/null 2>&1
openssl pkey -in "$TMP_DIR/ed25519.key" -pubout -out "$TMP_DIR/ed25519.pub" >/dev/null 2>&1
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out "$TMP_DIR/rsa.key" >/dev/null 2>&1
openssl pkey -in "$TMP_DIR/rsa.key" -pubout -out "$TMP_DIR/rsa.pub" >/dev/null 2>&1

if ! "$VALIDATOR" "$TMP_DIR/ed25519.pub" >/dev/null; then
    echo "Ed25519 public key was rejected" >&2
    exit 1
fi

if "$VALIDATOR" "$TMP_DIR/rsa.pub" >/dev/null 2>&1; then
	echo "RSA public key was accepted" >&2
	exit 1
fi

if [ -f "$ROOT_DIR/keys/ota_pubkey.dev.pem" ]; then
	cp "$ROOT_DIR/keys/ota_pubkey.dev.pem" "$TMP_DIR/renamed-production.pem"
	if "$VALIDATOR" "$TMP_DIR/renamed-production.pem" >/dev/null 2>&1; then
		echo "renamed development OTA public key was accepted" >&2
		exit 1
	fi
	if ! OTA_ALLOW_DEV_KEY=1 "$VALIDATOR" "$TMP_DIR/renamed-production.pem" >/dev/null; then
		echo "development OTA public key was rejected despite OTA_ALLOW_DEV_KEY=1" >&2
		exit 1
	fi
fi

echo "OTA public key validation tests passed"
