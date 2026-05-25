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
exit 0
