#!/usr/bin/env python3
"""Export Silero VAD 6.2 16 kHz CPU weights from the bundled TorchScript archive.

The output keeps the original SVLW LSTM/decoder prefix used by rknn_vad split
mode, then appends an SVCE extension with the Conv1d encoder weights for
cpu_vad. This keeps older split-mode readers compatible because they stop after
the decoder bias.
"""

from __future__ import annotations

import argparse
import struct
import zipfile
from pathlib import Path


ROOT = "VADr_v6_10_25_noths_re/data"
HIDDEN = 128

LSTM_DECODER_FILES = {
    "lstm_W": 11,
    "lstm_R": 12,
    "lstm_B_ih": 13,
    "lstm_B_hh": 14,
    "decoder_weight": 15,
    "decoder_bias": 16,
}

CONV_LAYERS = [
    # data index: weight, bias, in_channels, out_channels, kernel, stride, padding
    (3, 4, 129, 128, 3, 1, 1),
    (5, 6, 128, 64, 3, 2, 1),
    (7, 8, 64, 64, 3, 2, 1),
    (9, 10, 64, 128, 3, 1, 1),
]


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--jit", required=True, help="input silero_vad_6_2.jit path")
    parser.add_argument("--output", required=True, help="output combined weights path")
    return parser.parse_args()


def read_member(zf: zipfile.ZipFile, index: int) -> bytes:
    name = f"{ROOT}/{index}"
    try:
        return zf.read(name)
    except KeyError as exc:
        raise SystemExit(f"missing TorchScript member {name}") from exc


def expect_bytes(label: str, data: bytes, expected: int) -> None:
    if len(data) != expected:
        raise SystemExit(f"{label} has {len(data)} bytes, expected {expected}")


def main() -> int:
    args = parse_args()
    output = Path(args.output)
    output.parent.mkdir(parents=True, exist_ok=True)

    with zipfile.ZipFile(args.jit) as zf:
        lstm_w = read_member(zf, LSTM_DECODER_FILES["lstm_W"])
        lstm_r = read_member(zf, LSTM_DECODER_FILES["lstm_R"])
        bias_ih = read_member(zf, LSTM_DECODER_FILES["lstm_B_ih"])
        bias_hh = read_member(zf, LSTM_DECODER_FILES["lstm_B_hh"])
        decoder_weight = read_member(zf, LSTM_DECODER_FILES["decoder_weight"])
        decoder_bias = read_member(zf, LSTM_DECODER_FILES["decoder_bias"])

        expect_bytes("lstm_W", lstm_w, 4 * HIDDEN * HIDDEN * 4)
        expect_bytes("lstm_R", lstm_r, 4 * HIDDEN * HIDDEN * 4)
        expect_bytes("lstm_B_ih", bias_ih, 4 * HIDDEN * 4)
        expect_bytes("lstm_B_hh", bias_hh, 4 * HIDDEN * 4)
        expect_bytes("decoder_weight", decoder_weight, HIDDEN * 4)
        expect_bytes("decoder_bias", decoder_bias, 4)

        conv_payloads: list[tuple[tuple[int, int, int, int, int], bytes, bytes]] = []
        for weight_idx, bias_idx, in_ch, out_ch, kernel, stride, padding in CONV_LAYERS:
            weight = read_member(zf, weight_idx)
            bias = read_member(zf, bias_idx)
            expect_bytes(f"conv{len(conv_payloads)} weight", weight, out_ch * in_ch * kernel * 4)
            expect_bytes(f"conv{len(conv_payloads)} bias", bias, out_ch * 4)
            conv_payloads.append(((in_ch, out_ch, kernel, stride, padding), weight, bias))

    with output.open("wb") as out:
        out.write(b"SVLW")
        out.write(struct.pack("<III", 2, HIDDEN, HIDDEN))
        out.write(lstm_w)
        out.write(lstm_r)
        out.write(bias_ih + bias_hh)
        out.write(decoder_weight)
        out.write(decoder_bias)

        out.write(b"SVCE")
        out.write(struct.pack("<II", 1, len(conv_payloads)))
        for spec, weight, bias in conv_payloads:
            out.write(struct.pack("<IIIII", *spec))
            out.write(weight)
            out.write(bias)

    print(f"Wrote {output} ({output.stat().st_size} bytes)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
