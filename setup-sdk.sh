#!/bin/bash

if [ "$(uname -s)" != "Linux" ]; then
  echo "Error: Please setup sdk on Linux or Docker Linux." >&2
  exit 1
fi

git clone https://github.com/LuckfoxTECH/luckfox-pico.git /sdk
