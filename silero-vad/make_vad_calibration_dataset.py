#!/usr/bin/env python3
"""Generate a real-audio RKNN quantization dataset for the Silero VAD helper."""

from __future__ import annotations

import argparse
import math
import shutil
import wave
from dataclasses import dataclass
from pathlib import Path

import numpy as np
import onnxruntime as ort


@dataclass
class Record:
    source: str
    frame_index: int
    gain_db: float
    probability: float
    energy: float
    feature: np.ndarray
    state: np.ndarray


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--onnx", required=True, help="fixed feature-input ONNX model path")
    parser.add_argument("--output-dir", required=True, help="directory for .npy files and dataset.txt")
    parser.add_argument(
        "--input",
        action="append",
        default=[],
        help="16 kHz mono raw s16le, wav, or npy file; may be passed multiple times",
    )
    parser.add_argument("--sample-rate", type=int, default=16000, help="expected sample rate")
    parser.add_argument("--frame-samples", type=int, default=512, help="samples per VAD frame")
    parser.add_argument(
        "--max-audio-seconds",
        type=float,
        default=0.0,
        help="truncate each input to this many seconds; <=0 keeps full input",
    )
    parser.add_argument(
        "--reset-seconds",
        type=float,
        default=0.0,
        help="reset recurrent state every N seconds during calibration rollout; <=0 never resets within a file",
    )
    parser.add_argument(
        "--context-samples",
        type=int,
        default=0,
        help="previous-frame samples prepended before every frame; Silero VAD 6.2 uses 64 at 16 kHz",
    )
    parser.add_argument(
        "--gain-db",
        type=float,
        action="append",
        default=[0.0],
        help="gain augmentation in dB; may be passed multiple times",
    )
    parser.add_argument(
        "--max-frames",
        type=int,
        default=5000,
        help="maximum calibration frames to write; <=0 writes all frames",
    )
    parser.add_argument(
        "--add-zero-frames",
        type=int,
        default=64,
        help="additional all-zero frames to include as negative examples",
    )
    parser.add_argument(
        "--add-noise-frames",
        type=int,
        default=64,
        help="additional low-level white-noise frames to include as negative examples",
    )
    parser.add_argument("--noise-std", type=float, default=0.002, help="white-noise stddev")
    parser.add_argument("--seed", type=int, default=20260527, help="deterministic selection seed")
    parser.add_argument(
        "--replace",
        action="store_true",
        help="remove output directory before writing",
    )
    parser.add_argument(
        "--allow-resample",
        action="store_true",
        help="allow wav/npy inputs to be resampled to --sample-rate with scipy",
    )
    return parser.parse_args()


def load_audio(path: Path, sample_rate: int, allow_resample: bool) -> np.ndarray:
    suffix = path.suffix.lower()
    if suffix in {".raw", ".pcm", ".s16le"}:
        pcm = np.fromfile(path, dtype="<i2")
        return (pcm.astype(np.float32) / 32768.0).clip(-1.0, 1.0)
    if suffix == ".wav":
        return load_wav(path, sample_rate, allow_resample)
    if suffix == ".npy":
        data = np.load(path)
        data = np.asarray(data).reshape(-1)
        if data.dtype == np.int16:
            return (data.astype(np.float32) / 32768.0).clip(-1.0, 1.0)
        return data.astype(np.float32).clip(-1.0, 1.0)
    raise ValueError(f"unsupported audio file type: {path}")


def load_wav(path: Path, sample_rate: int, allow_resample: bool) -> np.ndarray:
    with wave.open(str(path), "rb") as wav:
        channels = wav.getnchannels()
        width = wav.getsampwidth()
        rate = wav.getframerate()
        frames = wav.getnframes()
        data = wav.readframes(frames)

    if width != 2:
        raise ValueError(f"{path} uses {width * 8}-bit PCM; expected 16-bit PCM")
    pcm = np.frombuffer(data, dtype="<i2").reshape(-1, channels)
    mono = pcm[:, 0].astype(np.float32) / 32768.0
    mono = mono.clip(-1.0, 1.0)
    if rate == sample_rate:
        return mono
    if not allow_resample:
        raise ValueError(f"{path} sample rate is {rate}; expected {sample_rate}")
    return resample(mono, rate, sample_rate)


def resample(samples: np.ndarray, orig_rate: int, target_rate: int) -> np.ndarray:
    from scipy.signal import resample_poly

    gcd = math.gcd(orig_rate, target_rate)
    up = target_rate // gcd
    down = orig_rate // gcd
    return resample_poly(samples, up, down).astype(np.float32).clip(-1.0, 1.0)


def silero_stft_magnitude(audio: np.ndarray) -> np.ndarray:
    n_fft = 256
    hop = 128
    padded = np.zeros((audio.shape[0] + n_fft // 4,), dtype=np.float32)
    padded[: audio.shape[0]] = audio
    for i in range(n_fft // 4):
        padded[audio.shape[0] + i] = padded[audio.shape[0] - 2 - i]
    window = (0.5 - 0.5 * np.cos((2.0 * np.pi / n_fft) * np.arange(n_fft))).astype(
        np.float32
    )
    frames = ((padded.shape[0] - n_fft) // hop) + 1
    feature = np.zeros((1, n_fft // 2 + 1, frames), dtype=np.float32)
    for i in range(frames):
        segment = padded[i * hop : i * hop + n_fft] * window
        feature[0, :, i] = np.abs(np.fft.rfft(segment, n=n_fft)).astype(np.float32)
    return feature


def make_session(model_path: str) -> ort.InferenceSession:
    opts = ort.SessionOptions()
    opts.inter_op_num_threads = 1
    opts.intra_op_num_threads = 1
    return ort.InferenceSession(model_path, sess_options=opts, providers=["CPUExecutionProvider"])


def append_rollout_records(
    records: list[Record],
    session: ort.InferenceSession,
    samples: np.ndarray,
    source: str,
    frame_samples: int,
    gain_db: float,
    reset_frames: int,
    context_samples: int,
) -> None:
    input_name = session.get_inputs()[0].name
    state_name = session.get_inputs()[1].name
    output_name = session.get_outputs()[0].name
    state_output_name = session.get_outputs()[1].name

    gain = 10.0 ** (gain_db / 20.0)
    state = np.zeros((2, 1, 128), dtype=np.float32)
    context = np.zeros((context_samples,), dtype=np.float32)
    frame_count = samples.shape[0] // frame_samples
    for frame_index in range(frame_count):
        if reset_frames > 0 and frame_index > 0 and frame_index % reset_frames == 0:
            state.fill(0.0)
            context.fill(0.0)
        start = frame_index * frame_samples
        frame = (samples[start : start + frame_samples] * gain).clip(-1.0, 1.0)
        if context_samples > 0:
            model_audio = np.concatenate([context, frame]).astype(np.float32, copy=False)
        else:
            model_audio = frame.astype(np.float32, copy=False)
        feature = silero_stft_magnitude(model_audio)
        prev_state = state.astype(np.float32, copy=True)
        prob, next_state = session.run(
            [output_name, state_output_name],
            {input_name: feature, state_name: prev_state},
        )
        state = np.asarray(next_state, dtype=np.float32)
        if context_samples > 0:
            context = frame[-context_samples:].astype(np.float32, copy=True)
        records.append(
            Record(
                source=source,
                frame_index=frame_index,
                gain_db=gain_db,
                probability=float(np.asarray(prob).reshape(-1)[0]),
                energy=float(np.sqrt(np.mean(frame * frame))),
                feature=feature,
                state=prev_state,
            )
        )


def append_synthetic_negatives(
    records: list[Record],
    session: ort.InferenceSession,
    zero_frames: int,
    noise_frames: int,
    noise_std: float,
    frame_samples: int,
    context_samples: int,
    rng: np.random.Generator,
) -> None:
    if zero_frames > 0:
        zeros = np.zeros(zero_frames * frame_samples, dtype=np.float32)
        append_rollout_records(
            records, session, zeros, "synthetic_zero", frame_samples, 0.0, 0, context_samples
        )
    if noise_frames > 0:
        noise = rng.normal(0.0, noise_std, noise_frames * frame_samples).astype(np.float32)
        append_rollout_records(
            records, session, noise, "synthetic_noise", frame_samples, 0.0, 0, context_samples
        )


def select_records(records: list[Record], max_frames: int, rng: np.random.Generator) -> list[Record]:
    if max_frames <= 0 or len(records) <= max_frames:
        return records

    selected: list[Record] = []
    selected_ids: set[int] = set()

    def take(candidates: list[int], count: int) -> None:
        if count <= 0:
            return
        candidates = [i for i in candidates if i not in selected_ids]
        if not candidates:
            return
        if len(candidates) > count:
            candidates = rng.choice(candidates, size=count, replace=False).tolist()
        for index in candidates:
            selected_ids.add(index)
            selected.append(records[index])

    order_by_prob = sorted(range(len(records)), key=lambda i: records[i].probability, reverse=True)
    take(order_by_prob[: max(1, max_frames // 5)], max_frames // 5)

    buckets = [
        [i for i, r in enumerate(records) if r.probability >= 0.5],
        [i for i, r in enumerate(records) if 0.1 <= r.probability < 0.5],
        [i for i, r in enumerate(records) if 0.01 <= r.probability < 0.1],
        [i for i, r in enumerate(records) if r.probability < 0.01],
    ]
    quota = max_frames // 5
    for bucket in buckets:
        take(bucket, quota)

    remaining = [i for i in range(len(records)) if i not in selected_ids]
    take(remaining, max_frames - len(selected))
    return selected[:max_frames]


def write_dataset(records: list[Record], output_dir: Path) -> None:
    dataset_path = output_dir / "dataset.txt"
    with dataset_path.open("w", encoding="utf-8") as dataset:
        for index, record in enumerate(records):
            input_path = output_dir / f"input_{index:05d}.npy"
            state_path = output_dir / f"state_{index:05d}.npy"
            np.save(input_path, record.feature.astype(np.float32, copy=False))
            np.save(state_path, record.state.astype(np.float32, copy=False))
            dataset.write(f"{input_path} {state_path}\n")

    probs = np.array([r.probability for r in records], dtype=np.float32)
    energies = np.array([r.energy for r in records], dtype=np.float32)
    state_abs = np.array([np.max(np.abs(r.state)) for r in records], dtype=np.float32)
    with (output_dir / "summary.txt").open("w", encoding="utf-8") as summary:
        summary.write(f"frames={len(records)}\n")
        if len(records) > 0:
            summary.write(
                "probability_min_p50_p90_p99_max="
                + " ".join(f"{v:.8f}" for v in np.percentile(probs, [0, 50, 90, 99, 100]))
                + "\n"
            )
            summary.write(
                "energy_min_p50_p90_p99_max="
                + " ".join(f"{v:.8f}" for v in np.percentile(energies, [0, 50, 90, 99, 100]))
                + "\n"
            )
            summary.write(
                "state_abs_min_p50_p90_p99_max="
                + " ".join(f"{v:.8f}" for v in np.percentile(state_abs, [0, 50, 90, 99, 100]))
                + "\n"
            )
        for source in sorted({r.source for r in records}):
            count = sum(1 for r in records if r.source == source)
            summary.write(f"source={source} frames={count}\n")


def main() -> int:
    args = parse_args()
    if not args.input:
        raise SystemExit("at least one --input is required")

    output_dir = Path(args.output_dir)
    if output_dir.exists() and args.replace:
        shutil.rmtree(output_dir)
    output_dir.mkdir(parents=True, exist_ok=True)
    output_dir = output_dir.resolve()

    rng = np.random.default_rng(args.seed)
    session = make_session(args.onnx)
    records: list[Record] = []
    reset_frames = 0
    if args.reset_seconds > 0:
        reset_frames = max(1, round(args.reset_seconds * args.sample_rate / args.frame_samples))
    for name in args.input:
        path = Path(name)
        samples = load_audio(path, args.sample_rate, args.allow_resample)
        if args.max_audio_seconds > 0:
            samples = samples[: round(args.max_audio_seconds * args.sample_rate)]
        for gain_db in args.gain_db:
            append_rollout_records(
                records,
                session,
                samples,
                path.name,
                args.frame_samples,
                gain_db,
                reset_frames,
                args.context_samples,
            )
    append_synthetic_negatives(
        records,
        session,
        args.add_zero_frames,
        args.add_noise_frames,
        args.noise_std,
        args.frame_samples,
        args.context_samples,
        rng,
    )

    selected = select_records(records, args.max_frames, rng)
    write_dataset(selected, output_dir)
    print(f"Wrote {len(selected)} calibration frames to {output_dir / 'dataset.txt'}")
    print((output_dir / "summary.txt").read_text(encoding="utf-8"), end="")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
