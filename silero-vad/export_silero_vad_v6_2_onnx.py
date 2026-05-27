#!/usr/bin/env python3
"""Export the Silero VAD 6.2 JIT model to an RKNN-friendly ONNX model."""

from __future__ import annotations

import argparse
from pathlib import Path

import onnx
import torch


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--jit", required=True, help="input silero_vad.jit path")
    parser.add_argument("--output", required=True, help="output ONNX path")
    parser.add_argument("--sample-rate", type=int, default=16000, choices=(8000, 16000))
    parser.add_argument("--frame-samples", type=int, default=512, help="external audio frame size")
    parser.add_argument(
        "--context-samples",
        type=int,
        default=64,
        help="Silero ONNX wrapper context samples prepended to every 16 kHz frame",
    )
    parser.add_argument("--state-size", type=int, default=128)
    parser.add_argument("--opset", type=int, default=18)
    return parser.parse_args()


class SileroV62Internal(torch.nn.Module):
    def __init__(self, jit_model: torch.jit.ScriptModule, sample_rate: int):
        super().__init__()
        self.jit_model = jit_model
        self.sample_rate = sample_rate

    def forward(self, x: torch.Tensor, state: torch.Tensor) -> tuple[torch.Tensor, torch.Tensor]:
        if self.sample_rate == 16000:
            return self.jit_model._model(x, state)
        return self.jit_model._model_8k(x, state)


@torch.no_grad()
def main() -> int:
    args = parse_args()
    if args.sample_rate == 8000 and args.context_samples == 64:
        args.context_samples = 32
    model = torch.jit.load(args.jit, map_location="cpu")
    model.eval()

    wrapped = SileroV62Internal(model, args.sample_rate)
    wrapped.eval()
    wrapped = torch.jit.script(wrapped)

    input_samples = args.frame_samples + args.context_samples
    x = torch.zeros((1, input_samples), dtype=torch.float32)
    state = torch.zeros((2, 1, args.state_size), dtype=torch.float32)

    output = Path(args.output)
    output.parent.mkdir(parents=True, exist_ok=True)
    torch.onnx.export(
        wrapped,
        (x, state),
        str(output),
        input_names=["input", "state"],
        output_names=["output", "stateN"],
        opset_version=args.opset,
        do_constant_folding=True,
    )

    onnx_model = onnx.load(str(output))
    metadata = {
        "model_type": "silero-vad-v6.2-internal",
        "sample_rate": args.sample_rate,
        "frame_samples": args.frame_samples,
        "context_samples": args.context_samples,
        "input_samples": input_samples,
        "state_shape": f"2,1,{args.state_size}",
    }
    del onnx_model.metadata_props[:]
    for key, value in metadata.items():
        prop = onnx_model.metadata_props.add()
        prop.key = key
        prop.value = str(value)
    onnx.save(onnx_model, str(output))
    onnx.checker.check_model(str(output))

    print(f"Exported {output}")
    for prop in onnx_model.metadata_props:
        print(f"{prop.key}={prop.value}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
