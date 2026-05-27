#!/usr/bin/env python3
"""Convert a Silero VAD ONNX model to an RV1106 RKNN model."""

import argparse
import copy
import sys
import tempfile
from pathlib import Path

import numpy as np
import onnx
from onnx import helper, numpy_helper


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--onnx", default="silero_vad.onnx", help="input Silero VAD ONNX path")
    parser.add_argument(
        "--output",
        default="silero_vad_rv1106.rknn",
        help="output RKNN model path",
    )
    parser.add_argument("--window-size", type=int, default=512, help="Silero input samples per frame")
    parser.add_argument(
        "--context-samples",
        type=int,
        default=0,
        help="audio context samples prepended before each frame; Silero VAD 6.2 uses 64 at 16 kHz",
    )
    parser.add_argument("--state-size", type=int, default=128, help="Silero recurrent state width")
    parser.add_argument("--sample-rate", type=int, default=16000, help="Silero sample rate")
    parser.add_argument(
        "--keep-input-pad",
        action="store_true",
        help="keep Silero's ONNX Pad op; not recommended for RV1106 runtime",
    )
    parser.add_argument(
        "--keep-stft-frontend",
        action="store_true",
        help="keep Silero's ONNX STFT/magnitude frontend; not recommended for RV1106 runtime",
    )
    parser.add_argument(
        "--fixed-onnx-output",
        default="",
        help="optional path to save the RKNN-ready fixed ONNX before conversion",
    )
    parser.add_argument(
        "--dump-fixed-onnx-only",
        action="store_true",
        help="write --fixed-onnx-output and exit without importing RKNN Toolkit2",
    )
    parser.add_argument(
        "--simplify-fixed-onnx",
        action="store_true",
        help="run onnxsim on the fixed ONNX; needed for Silero VAD 6.2 shape-driven If nodes",
    )
    parser.add_argument(
        "--dataset",
        default="",
        help="optional RKNN quantization dataset.txt; auto-generated when omitted",
    )
    parser.add_argument(
        "--calibration-frames",
        type=int,
        default=96,
        help="number of synthetic calibration frames when --dataset is omitted",
    )
    parser.add_argument(
        "--generic-onnx",
        action="store_true",
        help="do not pass Silero input names/shapes to rknn.load_onnx",
    )
    parser.add_argument(
        "--optimization-level",
        type=int,
        default=1,
        choices=range(0, 4),
        metavar="0..3",
        help="RKNN optimization_level; lower values convert faster, default: 1",
    )
    parser.add_argument(
        "--quantized-dtype",
        default="w8a8",
        choices=("w8a8", "w16a16i", "w16a16i_dfp", "w4a16"),
        help="RKNN quantized dtype passed to rknn.config; w8a16 is not supported on RV1106",
    )
    parser.add_argument(
        "--quantized-algorithm",
        default="normal",
        choices=("normal", "mmse", "kl_divergence", "gdq"),
        help="RKNN quantized algorithm passed to rknn.config",
    )
    parser.add_argument(
        "--quantized-method",
        default="channel",
        help="RKNN quantized method passed to rknn.config, e.g. channel, layer, group32",
    )
    parser.add_argument(
        "--auto-hybrid",
        action="store_true",
        help="enable RKNN automatic hybrid quantization during build",
    )
    parser.add_argument(
        "--no-quantization",
        action="store_true",
        help="build a non-quantized RKNN model; --dataset is ignored",
    )
    return parser.parse_args()


def write_synthetic_dataset(
    base_dir: Path,
    frame_count: int,
    window_size: int,
    context_samples: int,
    model_input_size: int,
    state_size: int,
    sample_rate: int,
    input_names: list[str],
    externalize_stft: bool,
) -> str:
    base_dir.mkdir(parents=True, exist_ok=True)
    dataset_path = base_dir / "dataset.txt"
    rng = np.random.default_rng(20260526)
    audio_input_size = window_size + context_samples
    t = np.arange(audio_input_size, dtype=np.float32) / float(sample_rate)

    with dataset_path.open("w", encoding="utf-8") as dataset:
        for i in range(frame_count):
            if i % 4 == 0:
                audio = np.zeros((1, audio_input_size), dtype=np.float32)
            elif i % 4 == 1:
                audio = rng.normal(0.0, 0.01, size=(1, audio_input_size)).astype(np.float32)
            elif i % 4 == 2:
                freq = 180.0 + 20.0 * (i % 11)
                audio = (0.12 * np.sin(2.0 * np.pi * freq * t))[None, :].astype(np.float32)
            else:
                freq = 420.0 + 30.0 * (i % 7)
                audio = (
                    0.08 * np.sin(2.0 * np.pi * freq * t)
                    + rng.normal(0.0, 0.015, size=audio_input_size)
                )[None, :].astype(np.float32)

            audio = np.clip(audio, -1.0, 1.0).astype(np.float32)
            if externalize_stft:
                audio = silero_stft_magnitude(audio, audio_input_size)
            elif model_input_size > audio_input_size:
                padded_audio = np.zeros((1, model_input_size), dtype=np.float32)
                padded_audio[:, :audio_input_size] = audio
                audio = padded_audio
            state = np.zeros((2, 1, state_size), dtype=np.float32)
            sr = np.array(sample_rate, dtype=np.int64)

            audio_path = base_dir / f"input_{i:04d}.npy"
            state_path = base_dir / f"state_{i:04d}.npy"
            sr_path = base_dir / f"sr_{i:04d}.npy"
            np.save(audio_path, audio)
            np.save(state_path, state)
            np.save(sr_path, sr)

            paths = {
                "input": audio_path,
                "state": state_path,
                "sr": sr_path,
            }
            dataset.write(" ".join(str(paths[name]) for name in input_names) + "\n")

    return str(dataset_path)


def silero_stft_magnitude(audio: np.ndarray, window_size: int) -> np.ndarray:
    n_fft = 256
    hop = 128
    freq_bins = n_fft // 2 + 1
    padded_size = window_size + n_fft // 4
    padded = np.zeros((padded_size,), dtype=np.float32)
    padded[:window_size] = audio.reshape(-1)[:window_size]
    for i in range(n_fft // 4):
        padded[window_size + i] = padded[window_size - 2 - i]
    n = np.arange(n_fft, dtype=np.float32)
    window = (0.5 - 0.5 * np.cos((2.0 * np.pi / n_fft) * n)).astype(np.float32)

    frames = ((padded_size - n_fft) // hop) + 1
    features = np.zeros((1, freq_bins, frames), dtype=np.float32)
    for frame in range(frames):
        segment = padded[frame * hop : frame * hop + n_fft] * window
        for freq in range(freq_bins):
            angle = (2.0 * np.pi * freq / n_fft) * n
            real = np.sum(segment * np.cos(angle, dtype=np.float32), dtype=np.float32)
            imag = np.sum(segment * -np.sin(angle, dtype=np.float32), dtype=np.float32)
            features[0, freq, frame] = np.sqrt(real * real + imag * imag, dtype=np.float32)
    return features


def tensor_shape(value_info: onnx.ValueInfoProto) -> list[int | str]:
    shape = []
    for dim in value_info.type.tensor_type.shape.dim:
        if dim.dim_value:
            shape.append(dim.dim_value)
        elif dim.dim_param:
            shape.append(dim.dim_param)
        else:
            shape.append("?")
    return shape


def set_tensor_shape(value_info: onnx.ValueInfoProto, dims: list[int]) -> None:
    shape = value_info.type.tensor_type.shape
    del shape.dim[:]
    for dim_value in dims:
        dim = shape.dim.add()
        dim.dim_value = dim_value


def remove_graph_inputs(model: onnx.ModelProto, names: set[str]) -> None:
    kept_inputs = [copy.deepcopy(value) for value in model.graph.input if value.name not in names]
    del model.graph.input[:]
    model.graph.input.extend(kept_inputs)


def prune_unused_initializers(model: onnx.ModelProto) -> None:
    used_inputs: set[str] = set()
    for node in model.graph.node:
        used_inputs.update(name for name in node.input if name)

    kept_initializers = [
        copy.deepcopy(initializer)
        for initializer in model.graph.initializer
        if initializer.name in used_inputs
    ]
    del model.graph.initializer[:]
    model.graph.initializer.extend(kept_initializers)


def collect_graph_inputs(graph: onnx.GraphProto) -> set[str]:
    used_inputs: set[str] = set()
    for node in graph.node:
        used_inputs.update(name for name in node.input if name)
        for attr in node.attribute:
            if attr.type == onnx.AttributeProto.GRAPH:
                used_inputs.update(collect_graph_inputs(attr.g))
            elif attr.type == onnx.AttributeProto.GRAPHS:
                for subgraph in attr.graphs:
                    used_inputs.update(collect_graph_inputs(subgraph))
    return used_inputs


def prune_unused_nodes(model: onnx.ModelProto) -> None:
    graph_outputs = {value.name for value in model.graph.output}
    changed = True
    while changed:
        used_inputs = collect_graph_inputs(model.graph)
        kept_nodes = []
        changed = False
        for node in model.graph.node:
            if any(output in graph_outputs or output in used_inputs for output in node.output):
                kept_nodes.append(copy.deepcopy(node))
            else:
                changed = True
        if changed:
            del model.graph.node[:]
            model.graph.node.extend(kept_nodes)


def simplify_onnx(model: onnx.ModelProto, input_shapes: dict[str, list[int]]) -> onnx.ModelProto:
    try:
        from onnxsim import simplify
    except ModuleNotFoundError as exc:
        if exc.name != "onnxsim":
            raise
        raise SystemExit(
            "Missing onnxsim. Install it or run this script from the sherpa silero_vad/v4 "
            "venv before using --simplify-fixed-onnx."
        ) from exc

    kwargs = {"overwrite_input_shapes": input_shapes}
    try:
        model_simp, check = simplify(model, **kwargs)
    except TypeError:
        model_simp, check = simplify(model, input_shapes=input_shapes)
    if not check:
        raise ValueError("onnxsim failed to validate the simplified ONNX model")
    return model_simp


def inline_silero_sample_rate_branch(model: onnx.ModelProto, sample_rate: int) -> None:
    if all(value.name != "sr" for value in model.graph.input):
        return

    if_node = next((node for node in model.graph.node if node.op_type == "If"), None)
    if if_node is None:
        return

    if sample_rate == 16000:
        branch_name = "then_branch"
    elif sample_rate == 8000:
        branch_name = "else_branch"
    else:
        raise ValueError(f"Silero ONNX only supports 8000 or 16000 Hz, got {sample_rate}")

    branches = {attr.name: attr.g for attr in if_node.attribute}
    if branch_name not in branches:
        raise ValueError(f"Silero ONNX If node does not contain {branch_name}")

    branch = branches[branch_name]
    if len(branch.output) != len(if_node.output):
        raise ValueError("Silero ONNX branch output count does not match If output count")

    new_nodes = [copy.deepcopy(node) for node in branch.node]
    for branch_output, graph_output_name in zip(branch.output, if_node.output):
        if branch_output.name != graph_output_name:
            new_nodes.append(
                helper.make_node(
                    "Identity",
                    inputs=[branch_output.name],
                    outputs=[graph_output_name],
                    name=f"inline_{graph_output_name}",
                )
            )

    del model.graph.node[:]
    model.graph.node.extend(new_nodes)
    remove_graph_inputs(model, {"sr"})
    prune_unused_initializers(model)


def initializer_array(model: onnx.ModelProto, name: str) -> np.ndarray | None:
    for initializer in model.graph.initializer:
        if initializer.name == name:
            return numpy_helper.to_array(initializer)
    return None


def remove_silero_input_pad(model: onnx.ModelProto, window_size: int) -> int:
    for index, node in enumerate(model.graph.node):
        if node.op_type != "Pad" or len(node.input) < 2 or node.input[0] != "input":
            continue

        pads = initializer_array(model, node.input[1])
        if pads is None:
            raise ValueError("Silero input Pad uses non-constant pads; cannot remove it safely")
        pads = pads.astype(np.int64).reshape(-1)
        if pads.size != 4 or pads[0] != 0 or pads[1] != 0 or pads[2] != 0 or pads[3] < 0:
            raise ValueError(f"unexpected Silero input Pad values: {pads.tolist()}")

        padded_size = window_size + int(pads[3])
        pad_output = node.output[0]
        for consumer in model.graph.node:
            for input_index, input_name in enumerate(consumer.input):
                if input_name == pad_output:
                    consumer.input[input_index] = "input"
        del model.graph.node[index]
        prune_unused_initializers(model)
        return padded_size

    return window_size


def stft_feature_shape(audio_input_size: int) -> tuple[int, int]:
    n_fft = 256
    hop = 128
    padded_size = audio_input_size + n_fft // 4
    if padded_size < n_fft:
        raise ValueError(f"audio input is too short for Silero STFT: {audio_input_size}")
    return n_fft // 2 + 1, ((padded_size - n_fft) // hop) + 1


def remove_silero_stft_frontend(model: onnx.ModelProto, audio_input_size: int) -> tuple[int, int]:
    sqrt_index = None
    sqrt_output = ""
    for index, node in enumerate(model.graph.node):
        if node.op_type == "Sqrt":
            sqrt_index = index
            sqrt_output = node.output[0]
            break
    if sqrt_index is None:
        return stft_feature_shape(audio_input_size)

    for consumer in model.graph.node[sqrt_index + 1 :]:
        for input_index, input_name in enumerate(consumer.input):
            if input_name == sqrt_output:
                consumer.input[input_index] = "input"

    kept_nodes = [
        copy.deepcopy(node)
        for node in model.graph.node[:sqrt_index]
        if node.op_type == "Constant"
    ]
    kept_nodes.extend(copy.deepcopy(node) for node in model.graph.node[sqrt_index + 1 :])
    del model.graph.node[:]
    model.graph.node.extend(kept_nodes)
    prune_unused_nodes(model)
    prune_unused_initializers(model)
    return stft_feature_shape(audio_input_size)


def fix_silero_onnx_for_rknn(
    model_path: str,
    output_dir: Path,
    window_size: int,
    context_samples: int,
    state_size: int,
    sample_rate: int,
    keep_input_pad: bool,
    keep_stft_frontend: bool,
    fixed_onnx_output: str,
    simplify_fixed_onnx: bool,
) -> tuple[str, list[str], list[list[int]], int]:
    model = onnx.load(model_path)
    inline_silero_sample_rate_branch(model, sample_rate)
    audio_input_size = window_size + context_samples
    model_input_shape = [1, audio_input_size]
    if not keep_stft_frontend:
        freq_bins, frames = remove_silero_stft_frontend(model, audio_input_size)
        model_input_shape = [1, freq_bins, frames]
    elif not keep_input_pad:
        model_input_size = remove_silero_input_pad(model, window_size)
        model_input_shape = [1, model_input_size]

    for value in model.graph.input:
        if value.name == "input":
            set_tensor_shape(value, model_input_shape)
        elif value.name == "state":
            set_tensor_shape(value, [2, 1, state_size])
    for value in model.graph.output:
        if value.name == "output":
            set_tensor_shape(value, [1, 1])
        elif value.name == "stateN":
            set_tensor_shape(value, [2, 1, state_size])

    input_names = [value.name for value in model.graph.input]
    input_shapes = [tensor_shape(value) for value in model.graph.input]
    if simplify_fixed_onnx:
        before_nodes = len(model.graph.node)
        model = simplify_onnx(
            model,
            {name: shape for name, shape in zip(input_names, input_shapes)},
        )
        print(f"--> Simplified ONNX nodes: {before_nodes} -> {len(model.graph.node)}")
        input_names = [value.name for value in model.graph.input]
        input_shapes = [tensor_shape(value) for value in model.graph.input]

    output_dir.mkdir(parents=True, exist_ok=True)
    if fixed_onnx_output:
        fixed_path = Path(fixed_onnx_output)
        fixed_path.parent.mkdir(parents=True, exist_ok=True)
    else:
        fixed_path = output_dir / (Path(model_path).stem + "_fixed.onnx")
    onnx.save(model, fixed_path)
    onnx.checker.check_model(str(fixed_path))

    print("--> ONNX inputs:")
    for value in model.graph.input:
        print(f"    {value.name}: {tensor_shape(value)}")
    print("--> ONNX outputs:")
    for value in model.graph.output:
        print(f"    {value.name}: {tensor_shape(value)}")

    return str(fixed_path), input_names, input_shapes, int(np.prod(model_input_shape))


def create_rknn() -> object:
    try:
        from rknn.api import RKNN
    except ModuleNotFoundError as exc:
        if exc.name not in {"rknn", "rknn.api"}:
            raise
        raise SystemExit(
            "Missing rknn-toolkit2. Install it in a Linux Python environment, for example:\n"
            "  python -m pip install rknn-toolkit2\n"
            "On macOS, run this script inside a Linux Docker container because RKNN Toolkit2 "
            "is distributed as Linux wheels."
        ) from exc
    return RKNN(verbose=True)


def main() -> int:
    args = parse_args()
    temp_dataset_dir = None
    temp_model_dir = None
    model_input_names = ["input", "state", "sr"]
    model_input_shapes = [[1, args.window_size], [2, 1, args.state_size], []]
    model_input_size = args.window_size
    if not args.generic_onnx:
        if args.fixed_onnx_output:
            output_dir = Path(args.fixed_onnx_output).parent
        else:
            temp_model_dir = tempfile.TemporaryDirectory(prefix="silero_vad_fixed_onnx_")
            output_dir = Path(temp_model_dir.name)
        fixed_onnx, model_input_names, model_input_shapes, model_input_size = fix_silero_onnx_for_rknn(
            args.onnx,
            output_dir,
            args.window_size,
            args.context_samples,
            args.state_size,
            args.sample_rate,
            args.keep_input_pad,
            args.keep_stft_frontend,
            args.fixed_onnx_output,
            args.simplify_fixed_onnx,
        )
        if args.dump_fixed_onnx_only:
            if not args.fixed_onnx_output:
                print(f"Fixed ONNX written to: {fixed_onnx}")
            if temp_model_dir is not None:
                temp_model_dir.cleanup()
            return 0
    elif args.dump_fixed_onnx_only:
        raise SystemExit("--dump-fixed-onnx-only requires Silero preprocessing; remove --generic-onnx")

    rknn = create_rknn()
    try:
        rknn.config(
            mean_values=None,
            std_values=None,
            target_platform="rv1106",
            optimization_level=args.optimization_level,
            quantized_dtype=args.quantized_dtype,
            quantized_algorithm=args.quantized_algorithm,
            quantized_method=args.quantized_method,
        )

        print(f"--> Loading ONNX model: {args.onnx}")
        if args.generic_onnx:
            ret = rknn.load_onnx(model=args.onnx)
        else:
            ret = rknn.load_onnx(
                model=fixed_onnx,
                inputs=model_input_names,
                input_size_list=model_input_shapes,
            )
        if ret != 0:
            print("Load model failed", file=sys.stderr)
            return ret

        do_quantization = not args.no_quantization
        dataset = args.dataset
        if do_quantization and not dataset:
            temp_dataset_dir = tempfile.TemporaryDirectory(prefix="silero_vad_rknn_dataset_")
            dataset = write_synthetic_dataset(
                Path(temp_dataset_dir.name),
                args.calibration_frames,
                args.window_size,
                args.context_samples,
                model_input_size,
                args.state_size,
                args.sample_rate,
                model_input_names if not args.generic_onnx else ["input", "state", "sr"],
                not args.generic_onnx and not args.keep_stft_frontend,
            )
            print(f"--> Generated quantization dataset: {dataset}")
        elif not do_quantization:
            dataset = None

        if do_quantization:
            print(
                f"--> Building {args.quantized_dtype} quantized RKNN model for RV1106"
            )
        else:
            print("--> Building non-quantized RKNN model for RV1106")
        ret = rknn.build(
            do_quantization=do_quantization,
            dataset=dataset,
            auto_hybrid=args.auto_hybrid,
        )
        if ret != 0:
            print("Build model failed", file=sys.stderr)
            return ret

        print(f"--> Exporting RKNN model: {args.output}")
        ret = rknn.export_rknn(args.output)
        if ret != 0:
            print("Export model failed", file=sys.stderr)
            return ret

        print("Conversion complete")
        return 0
    finally:
        if temp_dataset_dir is not None:
            temp_dataset_dir.cleanup()
        if temp_model_dir is not None:
            temp_model_dir.cleanup()
        rknn.release()


if __name__ == "__main__":
    raise SystemExit(main())
