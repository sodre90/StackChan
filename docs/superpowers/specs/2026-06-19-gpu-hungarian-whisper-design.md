# GPU-Accelerated Hungarian Whisper (≤2 GB VRAM, Qwen untouched)

**Date:** 2026-06-19
**Status:** Design approved, pending spec review
**Host:** home server `192.168.1.160` (NVIDIA RTX 5070 Ti, 16.3 GB VRAM)

## Problem

ASR latency is the driver. The live Whisper service is the **CPU** turbo image
(`stackchan-whisper-en`, faster-whisper `int8` on CPU, port 13000). Hungarian
transcription on CPU is too slow. We want GPU acceleration for Hungarian, but the
GPU is nearly full: Qwen's `llama-server` holds **14.16 GB**, leaving only
**~1.68 GB allocatable-free** (the 16303 − 14160 = ~2.1 GB gap minus ~0.46 GB CUDA/BAR
reserved overhead). A prior attempt to share the GPU starved Qwen into
`CUBLAS_STATUS_ALLOC_FAILED`, so Whisper was parked on CPU.

## Goals / Non-goals

**Goals**
- Run Hungarian Whisper on the GPU with materially lower latency than the CPU turbo.
- Keep real GPU allocation **< ~1.4 GB** (under the user's 2 GB ceiling and within the
  ~1.68 GB allocatable-free), so it coexists with Qwen.
- Pick the Hungarian model by **A/B benchmark** (accuracy → VRAM → latency).

**Non-goals**
- Do **not** touch Qwen's `llama-server` config or footprint.
- No changes to the xiaozhi protocol, the Go server, TTS, or the device firmware.
- Not solving multi-model hot-swapping or autoscaling.

## Constraints (locked with user)

| Constraint | Value |
|---|---|
| Primary driver | Latency (CPU turbo too slow) |
| VRAM ceiling | ≤ 2 GB; target real allocation < ~1.4 GB |
| Qwen | Untouched (don't shrink, don't restart) |
| Language | Hungarian-focused |
| Model selection | Open — decide by A/B bench |

## Architecture

Data flow is unchanged; only the Whisper **backend** moves CPU → GPU:

```
device → stackchan (Go, :12800) → POST /v1/audio/transcriptions (whisper:13000) → text → LLM
```

The existing `whisper_server.py` already supports everything needed: `--device cuda`,
`--compute-type int8_float16`, runtime CTranslate2 conversion, pre-quantized subfolder
loading, and `--feature-size` override. `Dockerfile.hu.cuda.turbo` already targets the
Hungarian model on CUDA. So the work is **validation + wiring + a bench**, not new
serving code.

## Components

### 1. GPU Whisper service
- Base image: `nvidia/cuda:12.6.3-cudnn-runtime-ubuntu22.04` (cuDNN 9 matches
  CTranslate2 ≥ 4.5).
- Runtime: faster-whisper, CUDA, `int8_float16`.
- **Change:** lower `beam_size` 10 → 5 in `whisper_server.py`'s `transcribe()` call.
  Beam 10 inflates the transient CUDA compute buffer (the part that risks Qwen OOM)
  and adds latency for little Hungarian accuracy gain. Keep `vad_filter=True` and
  `condition_on_previous_text=False`.
- GPU access via podman CDI: `--device nvidia.com/gpu=all` (matches how `qwen-llm`
  is launched).
- Model weights and the CTranslate2 conversion cache persist in the existing
  `whisper-models` and `whisper-ct2` named volumes so first-run conversion happens once.

### 2. A/B benchmark harness
A script (extends the existing `server/test_whisper_service.py` pattern) that, for each
candidate model, POSTs a fixed set of Hungarian clips and reports **transcript +
wall-clock latency + peak VRAM delta** (sampled via `nvidia-smi` around the call).

Candidates:
- **A:** `Maxdorger29/whisper-large-v3-turbo-hungarian-lora` (existing HU-tuned turbo)
- **B:** `deepdml/faster-whisper-large-v3-turbo-ct2` (pre-quantized generic turbo, no
  runtime conversion, multilingual)

**Ground truth:** the current `whisper/test-voice.mp3` is English (~2 s), unusable for
Hungarian accuracy. Generate controlled Hungarian ground-truth by synthesizing 3–5 known
Hungarian sentences through the **live TTS service** (`tts:14000`, `hu-HU-NoemiNeural`),
saving each `(audio, reference_text)` pair. Bench transcribes them back and computes WER
against the reference. The user (native speaker) makes the final accuracy call; WER is the
tie-breaker.

### 3. Compose wiring
`server/podman-compose.hu.yml`'s `whisper` service currently builds
`whisper/Dockerfile.hu.cpu.turbo`. Point it at the CUDA dockerfile (parameterized by the
winning model) and add the GPU device. The CPU turbo image remains the **instant
rollback** (one-line compose revert).

### 4. Coexistence proof (the actual risk)
After the GPU service is up with the chosen model:
1. `nvidia-smi` — confirm total used stays < 16.3 GB with margin; record Whisper's
   resident allocation.
2. **Concurrent load:** drive Qwen generation (`/v1/chat/completions` on :8080) *while*
   transcribing Hungarian clips in a loop. Confirm:
   - no `CUBLAS_STATUS_ALLOC_FAILED` in either container's logs,
   - Qwen tokens/sec within ~10 % of its solo baseline,
   - Whisper transcriptions still correct.

## Decision gate

Pick A or B by: **(1) Hungarian accuracy** (user judgment + WER), then **(2) real VRAM
< ~1.4 GB**, then **(3) latency**. If both blow the VRAM budget under concurrent load,
fall back to a smaller Hungarian-capable model (distil-large-v3 / medium) — recorded as
an explicit contingency, not part of the happy path.

## Success criteria

- [ ] GPU Whisper transcribes Hungarian correctly (user-accepted on the bench clips).
- [ ] Resident VRAM allocation **< ~1.4 GB**; total GPU usage leaves headroom < 16.3 GB.
- [ ] Concurrent Qwen + Whisper: **no OOM**, Qwen perf within ~10 % of baseline.
- [ ] ASR latency on GPU **materially better** than the CPU turbo for the same clips
      (target: sub-second for short utterances).
- [ ] Live HU stack (`podman-compose.hu.yml`) serves the GPU model; CPU rollback verified
      to still work.

## Risks & mitigations

| Risk | Mitigation |
|---|---|
| Qwen OOM when Whisper allocates compute buffers | beam_size→5; coexistence test before declaring done; CPU rollback ready |
| Maxdorger29 is a LoRA — CTranslate2 runtime conversion may fail or mis-merge | Bench validates load + output; B (pre-quantized) is the packaged fallback |
| First-run CT2 conversion slow / re-runs each restart | Persist `whisper-ct2` volume; verify cache hit on second start |
| `nvidia-smi` free (1.68 GB) < the naive 2.1 GB gap | Target < ~1.4 GB real allocation, not the gap |
| Deploy serves stale image (known GitOps gotcha) | `build --no-cache` + grep a unique marker in the image before testing |

## Rollback

Revert `podman-compose.hu.yml`'s `whisper` service to `Dockerfile.hu.cpu.turbo` and
`up -d --force-recreate whisper`. Qwen and everything else are untouched throughout.
