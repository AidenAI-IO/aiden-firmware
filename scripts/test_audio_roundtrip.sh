#!/bin/bash
# Test audio roundtrip: generate audio with gpt-4o-audio-preview, then send it back as input
# Usage:
#   export OPENROUTER_KEY=sk-or-...
#   export HTTP_PROXY=http://127.0.0.1:7890   # optional
#   export HTTPS_PROXY=http://127.0.0.1:7890  # optional
#   ./scripts/test_audio_roundtrip.sh

set -e

API_KEY="${OPENROUTER_KEY:-}"
if [ -z "$API_KEY" ]; then
    echo "Error: set OPENROUTER_KEY env var" >&2
    exit 1
fi

# Optional HTTP(S) proxy. Accept either HTTP_PROXY/HTTPS_PROXY or lower-case
# variants; pass whichever is set through to curl via -x.
PROXY="${HTTPS_PROXY:-${https_proxy:-${HTTP_PROXY:-${http_proxy:-}}}}"
CURL_PROXY_ARGS=()
if [ -n "$PROXY" ]; then
    CURL_PROXY_ARGS=(-x "$PROXY")
    echo "Using proxy: $PROXY"
fi

URL="https://openrouter.ai/api/v1/chat/completions"
MODEL="openai/gpt-4o-audio-preview"

echo "=== Step 1: Generate audio from text (streaming, pcm16) ==="
cat > /tmp/gen_req.json <<EOF
{
  "model": "$MODEL",
  "modalities": ["text", "audio"],
  "audio": {"voice": "alloy", "format": "pcm16"},
  "stream": true,
  "messages": [
    {"role": "user", "content": "Please say exactly: The quick brown fox jumps over the lazy dog."}
  ]
}
EOF

curl -s -N -X POST "$URL" \
  "${CURL_PROXY_ARGS[@]}" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d @/tmp/gen_req.json > /tmp/gen_stream.txt

grep '^data: ' /tmp/gen_stream.txt \
  | sed 's/^data: //' \
  | grep -v '^\[DONE\]$' \
  | jq -r 'try .choices[0].delta.audio.data // empty' \
  | tr -d '\n' > /tmp/generated.b64

TRANSCRIPT=$(grep '^data: ' /tmp/gen_stream.txt \
  | sed 's/^data: //' \
  | grep -v '^\[DONE\]$' \
  | jq -r 'try .choices[0].delta.audio.transcript // empty' \
  | tr -d '\n')

if [ ! -s /tmp/generated.b64 ]; then
    echo "Error: no audio in stream. Raw stream:"
    cat /tmp/gen_stream.txt
    exit 1
fi

base64 -d -i /tmp/generated.b64 > /tmp/generated.pcm
PCM_SIZE=$(wc -c < /tmp/generated.pcm)
echo "Generated PCM: $PCM_SIZE bytes"
echo "Transcript: $TRANSCRIPT"

# Wrap PCM (24kHz, mono, 16-bit) in WAV header
python3 - <<PYEOF
import struct
with open("/tmp/generated.pcm", "rb") as f:
    pcm = f.read()
sr, ch, bw = 24000, 1, 16
byte_rate = sr * ch * bw // 8
block_align = ch * bw // 8
data_size = len(pcm)
with open("/tmp/generated.wav", "wb") as f:
    f.write(b"RIFF")
    f.write(struct.pack("<I", 36 + data_size))
    f.write(b"WAVEfmt ")
    f.write(struct.pack("<IHHIIHH", 16, 1, ch, sr, byte_rate, block_align, bw))
    f.write(b"data")
    f.write(struct.pack("<I", data_size))
    f.write(pcm)
PYEOF
echo "Wrapped WAV: $(wc -c < /tmp/generated.wav) bytes"
file /tmp/generated.wav

echo ""
echo "=== Step 2: Send generated audio back as input ==="
AUDIO_BASE64=$(base64 -i /tmp/generated.wav | tr -d '\n')

cat > /tmp/recog_req.json <<EOF
{
  "model": "$MODEL",
  "modalities": ["text"],
  "messages": [
    {"role": "user", "content": [
      {"type": "input_audio", "input_audio": {"data": "$AUDIO_BASE64", "format": "wav"}},
      {"type": "text", "text": "What did I just say? Repeat it exactly."}
    ]}
  ]
}
EOF

curl -s -X POST "$URL" \
  "${CURL_PROXY_ARGS[@]}" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d @/tmp/recog_req.json > /tmp/recog_resp.json

echo "Response:"
jq '.choices[0].message.content // .' /tmp/recog_resp.json
