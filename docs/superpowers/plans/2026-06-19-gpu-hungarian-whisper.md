# GPU-Accelerated Hungarian Whisper Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move Hungarian Whisper ASR from CPU to the RTX 5070 Ti GPU at < ~1.4 GB VRAM, picking the model by A/B benchmark, without disturbing the Qwen `llama-server` that holds 14.16 GB.

**Architecture:** faster-whisper on CUDA `int8_float16` served by the existing `whisper_server.py`. A stdlib-only bench harness generates Hungarian ground-truth via the live TTS service, transcribes it on each candidate model, and reports WER + latency + VRAM. The winner is wired into `podman-compose.hu.yml` and proven to coexist with Qwen under concurrent load. CPU image stays as instant rollback.

**Tech Stack:** Python 3 (faster-whisper, CTranslate2, FastAPI), podman / podman-compose, CUDA 12.6 + cuDNN 9, edge-tts, the Go `stackchan` server (unchanged).

## Global Constraints

- VRAM ceiling: **≤ 2 GB**; target real resident allocation **< ~1.4 GB** (only ~1.68 GB is allocatable-free).
- Qwen `llama-server` (container `qwen-llm`, :8080, 14.16 GB) is **NOT** touched, restarted, or reconfigured.
- No changes to the xiaozhi protocol, the Go server, TTS, or device firmware.
- Compute type: `int8_float16`. Mel feature size: `128` (large-v3 family).
- Live whisper on port **13000** stays serving during the bench; benchmarking uses a temporary port **13001**.
- GPU access via podman CDI: `nvidia.com/gpu=all` (same mechanism as `qwen-llm`).
- Deploy gotcha: a Go/source change needs `build --no-cache`; verify the running image with a unique grep marker before testing (per `servers_and_services` memory).
- All server-side commands run over `ssh 192.168.1.160` against `~/StackChan`.

---

### Task 1: Reduce Whisper beam_size and rebuild the GPU image

Beam 10 inflates the transient CUDA compute buffer (the part that risks Qwen OOM) and adds latency for negligible Hungarian gain. Drop to 5.

**Files:**
- Modify: `server/whisper_server.py` (the `model.transcribe(...)` call, ~line 57-65)

**Interfaces:**
- Produces: a `whisper_server.py` whose `transcribe()` uses `beam_size=5`, baked into a freshly built `localhost/stackchan-whisper-hu-cuda-turbo` image used by Tasks 3–6.

- [ ] **Step 1: Edit the beam size**

In `server/whisper_server.py`, change the `model.transcribe` call:

```python
        segments, info = model.transcribe(
            tmp_path,
            language=language,
            task=task,
            beam_size=5,
            vad_filter=True,
            condition_on_previous_text=False,
            initial_prompt=initial_prompt,
        )
```

- [ ] **Step 2: Verify the change locally**

Run: `grep -n "beam_size" server/whisper_server.py`
Expected: `beam_size=5,`

- [ ] **Step 3: Commit**

```bash
git add server/whisper_server.py
git commit -m "perf(whisper): beam_size 10->5 to shrink CUDA compute buffer and latency"
```

- [ ] **Step 4: Push and build the CUDA image on the server**

```bash
git push origin main
ssh 192.168.1.160 'cd ~/StackChan && git pull && \
  podman-compose -f server/podman-compose.hu.yml build --no-cache whisper 2>&1 | tail -5 || \
  podman build --no-cache -t localhost/stackchan-whisper-hu-cuda-turbo -f server/whisper/Dockerfile.hu.cuda.turbo server'
```

Note: `podman-compose.hu.yml` still points `whisper` at the CPU dockerfile at this stage, so build the CUDA image directly with the second command. Run:

```bash
ssh 192.168.1.160 'cd ~/StackChan/server && podman build --no-cache \
  -t localhost/stackchan-whisper-hu-cuda-turbo \
  -f whisper/Dockerfile.hu.cuda.turbo .'
```
Expected: build completes, ends with a new image id.

- [ ] **Step 5: Verify the new image carries beam_size=5**

Run:
```bash
ssh 192.168.1.160 'podman run --rm localhost/stackchan-whisper-hu-cuda-turbo grep -n "beam_size" whisper_server.py'
```
Expected: `beam_size=5,`

---

### Task 2: Hungarian ground-truth bench harness

A stdlib-only script (no pip installs on the server) that synthesizes known Hungarian sentences through the live TTS service, transcribes them on a candidate Whisper URL, and reports WER + latency + GPU memory. Uses `curl` via subprocess for HTTP (robust multipart) and `nvidia-smi` for VRAM.

**Files:**
- Create: `server/bench_whisper_hu.py`
- Test: `server/test_bench_wer.py`

**Interfaces:**
- Produces: `bench_whisper_hu.py` with a `wer(ref: str, hyp: str) -> float` function (word-level Levenshtein, Hungarian-aware normalization) and a CLI `--whisper-url`, `--tts-url`, `--out-dir`.

- [ ] **Step 1: Write the failing WER unit test**

Create `server/test_bench_wer.py`:

```python
from bench_whisper_hu import wer

def test_identical_is_zero():
    assert wer("Kapcsold fel a lámpát", "Kapcsold fel a lámpát") == 0.0

def test_punctuation_and_case_ignored():
    assert wer("Hány óra van?", "hány óra van") == 0.0

def test_one_word_substitution():
    # 1 substitution over 4 reference words
    assert abs(wer("kapcsold fel a lámpát", "kapcsold le a lámpát") - 0.25) < 1e-9

def test_empty_hyp_is_total_error():
    assert wer("egy két három", "") == 1.0
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd server && python3 -m pytest test_bench_wer.py -v`
Expected: FAIL — `ModuleNotFoundError: No module named 'bench_whisper_hu'`

- [ ] **Step 3: Implement the bench harness**

Create `server/bench_whisper_hu.py`:

```python
#!/usr/bin/env python3
"""A/B benchmark for Hungarian Whisper: synthesizes known Hungarian sentences via
the live TTS service, transcribes them on a candidate Whisper URL, reports WER +
latency + GPU memory delta. Stdlib + curl + nvidia-smi only (no pip installs)."""

import argparse, json, os, re, subprocess, time

SENTENCES = [
    "Kapcsold fel a nappali lámpát.",
    "Hány óra van most Budapesten?",
    "Milyen lesz a holnapi időjárás?",
    "Játssz egy kis zenét a konyhában.",
    "Emlékeztess, hogy tíz perc múlva indulnom kell.",
]

def normalize(s):
    s = s.lower()
    s = re.sub(r"[^0-9a-záéíóöőúüű ]+", " ", s)
    return s.split()

def wer(ref, hyp):
    r, h = normalize(ref), normalize(hyp)
    n, m = len(r), len(h)
    if n == 0:
        return 0.0 if m == 0 else 1.0
    d = list(range(m + 1))
    for i in range(1, n + 1):
        prev, d[0] = d[0], i
        for j in range(1, m + 1):
            cur = d[j]
            cost = 0 if r[i - 1] == h[j - 1] else 1
            d[j] = min(d[j] + 1, d[j - 1] + 1, prev + cost)
            prev = cur
    return d[m] / n

def gpu_used_mib():
    out = subprocess.check_output(
        ["nvidia-smi", "--query-gpu=memory.used", "--format=csv,noheader,nounits"]
    ).decode().strip().splitlines()
    return int(out[0])

def tts(text, voice, tts_url, path):
    payload = json.dumps({"input": text, "voice": voice, "response_format": "opus"})
    subprocess.check_call([
        "curl", "-sS", "-X", "POST", f"{tts_url}/v1/audio/speech",
        "-H", "Content-Type: application/json", "-d", payload, "-o", path,
    ])

def transcribe(path, whisper_url):
    out = subprocess.check_output([
        "curl", "-sS", "-X", "POST", f"{whisper_url}/v1/audio/transcriptions",
        "-F", f"file=@{path};filename=clip.ogg", "-F", "language=hu",
    ]).decode()
    return json.loads(out).get("text", "")

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--whisper-url", default="http://localhost:13001")
    ap.add_argument("--tts-url", default="http://localhost:14000")
    ap.add_argument("--voice", default="hu-HU-NoemiNeural")
    ap.add_argument("--out-dir", default="/tmp/whisper-bench")
    args = ap.parse_args()
    os.makedirs(args.out_dir, exist_ok=True)

    idle = gpu_used_mib()
    print(f"GPU used (candidate idle): {idle} MiB")
    rows, peak = [], idle
    for i, text in enumerate(SENTENCES):
        clip = os.path.join(args.out_dir, f"clip_{i}.ogg")
        tts(text, args.voice, args.tts_url, clip)
        t0 = time.time()
        hyp = transcribe(clip, args.whisper_url)
        dt = time.time() - t0
        peak = max(peak, gpu_used_mib())
        e = wer(text, hyp)
        rows.append((e, dt))
        print(f"[{i}] WER={e:.3f} {dt*1000:6.0f}ms ref={text!r} hyp={hyp!r}")

    avg_wer = sum(r[0] for r in rows) / len(rows)
    avg_ms = sum(r[1] for r in rows) / len(rows) * 1000
    print("-" * 60)
    print(f"avg WER: {avg_wer:.3f} | avg latency: {avg_ms:.0f} ms | "
          f"peak GPU: {peak} MiB (candidate footprint ~{peak - idle} MiB over idle)")

if __name__ == "__main__":
    main()
```

- [ ] **Step 4: Run the WER test to verify it passes**

Run: `cd server && python3 -m pytest test_bench_wer.py -v`
Expected: 4 passed.

- [ ] **Step 5: Commit**

```bash
git add server/bench_whisper_hu.py server/test_bench_wer.py
git commit -m "test(whisper): Hungarian A/B bench harness (WER + latency + VRAM)"
git push origin main
```

---

### Task 3: Run the A/B benchmark on GPU (live :13000 untouched)

Run each candidate on a temporary port 13001 with GPU access, bench it, record results, decide the winner. The live CPU whisper on :13000 keeps serving.

**Files:** none modified (produces a results note).

**Interfaces:**
- Consumes: the image from Task 1, the bench from Task 2.
- Produces: a decision — model **A** (`Maxdorger29/whisper-large-v3-turbo-hungarian-lora`) or **B** (`deepdml/faster-whisper-large-v3-turbo-ct2`) — used in Task 4.

- [ ] **Step 1: Pull latest on the server**

```bash
ssh 192.168.1.160 'cd ~/StackChan && git pull'
```

- [ ] **Step 2: Record Qwen-only VRAM baseline**

```bash
ssh 192.168.1.160 'nvidia-smi --query-gpu=memory.used,memory.free --format=csv'
```
Expected: ~14160 MiB used. Note this number.

- [ ] **Step 3: Start candidate A on port 13001 with GPU**

```bash
ssh 192.168.1.160 'podman run -d --rm --name whisper-bench \
  --device nvidia.com/gpu=all --security-opt label=disable \
  -p 13001:13000 \
  -v whisper-models:/root/.cache/huggingface \
  -v whisper-ct2:/root/.cache/ct2_models \
  localhost/stackchan-whisper-hu-cuda-turbo \
  python3 whisper_server.py \
    --model Maxdorger29/whisper-large-v3-turbo-hungarian-lora \
    --device cuda --compute-type int8_float16 --feature-size 128 --port 13000'
```

- [ ] **Step 4: Wait for model load, confirm health**

```bash
ssh 192.168.1.160 'for i in $(seq 1 60); do \
  curl -sf http://localhost:13001/health && break; sleep 5; done; echo'
```
Expected: `{"status":"ok"}` (first run may convert the model to CTranslate2 — can take minutes; the `whisper-ct2` volume caches it).

- [ ] **Step 5: Run the bench for A**

```bash
ssh 192.168.1.160 'cd ~/StackChan/server && python3 bench_whisper_hu.py \
  --whisper-url http://localhost:13001 --tts-url http://localhost:14000'
```
Expected: a per-sentence table plus `avg WER`, `avg latency`, `peak GPU` / `candidate footprint`. Record all of it.

- [ ] **Step 6: Stop candidate A**

```bash
ssh 192.168.1.160 'podman stop whisper-bench'
```

- [ ] **Step 7: Repeat Steps 3–6 for candidate B**

Same as Step 3 but `--model deepdml/faster-whisper-large-v3-turbo-ct2` (already CTranslate2 format — no runtime conversion, faster first start). Then Steps 4–6.

- [ ] **Step 8: Decide the winner**

Apply the decision gate: **(1) Hungarian accuracy** (lower avg WER + user's read of the transcripts) → **(2) footprint < ~1.4 GB (< ~1400 MiB)** → **(3) latency**. If BOTH exceed the VRAM budget under load, switch to the contingency model `distil-whisper/distil-large-v3` (smaller) and re-bench. Write the chosen model id into Task 4.

---

### Task 4: Wire the winning model into the HU compose stack

Point the compose `whisper` service at the CUDA dockerfile with GPU access and the winning model.

**Files:**
- Modify: `server/whisper/Dockerfile.hu.cuda.turbo` (only if winner is B — change the `--model` in CMD)
- Modify: `server/podman-compose.hu.yml` (whisper service)

**Interfaces:**
- Consumes: the winning model id from Task 3.
- Produces: a `whisper` service that runs on GPU and is reachable at `whisper:13000` inside the compose network (unchanged for `stackchan`).

- [ ] **Step 1: If winner is B, set the model in the dockerfile CMD**

In `server/whisper/Dockerfile.hu.cuda.turbo`, set the CMD model line to the winner. For B:

```dockerfile
CMD ["python3", "whisper_server.py", \
     "--model", "deepdml/faster-whisper-large-v3-turbo-ct2", \
     "--device", "cuda", \
     "--compute-type", "int8_float16", \
     "--feature-size", "128", \
     "--port", "13000"]
```

(If winner is A, leave the existing `Maxdorger29/...` CMD unchanged.)

- [ ] **Step 2: Point compose at the CUDA dockerfile + GPU**

In `server/podman-compose.hu.yml`, replace the `whisper` service block with:

```yaml
  whisper:
    build:
      context: .
      dockerfile: whisper/Dockerfile.hu.cuda.turbo
    image: localhost/stackchan-whisper-hu-cuda-turbo
    ports:
      - "13000:13000"
    devices:
      - "nvidia.com/gpu=all"
    security_opt:
      - "label=disable"
    volumes:
      - whisper-models:/root/.cache/huggingface
      - whisper-ct2:/root/.cache/ct2_models
    restart: unless-stopped
```

- [ ] **Step 3: Update the header comment**

In `server/podman-compose.hu.yml`, change the top comment line that says the whisper model is `whisper/Dockerfile.hu.cpu.turbo — CPU` to note it now uses `whisper/Dockerfile.hu.cuda.turbo — GPU (int8_float16, <~1.4GB VRAM)`.

- [ ] **Step 4: Commit and push**

```bash
git add server/podman-compose.hu.yml server/whisper/Dockerfile.hu.cuda.turbo
git commit -m "feat(whisper): serve Hungarian ASR on GPU in the HU compose stack"
git push origin main
```

---

### Task 5: Coexistence proof under concurrent Qwen + Whisper load

The actual risk. Deploy GPU whisper into the live stack, then hammer Qwen and Whisper simultaneously and confirm no OOM and Qwen perf holds.

**Files:**
- Create: `server/test_coexistence.sh`

**Interfaces:**
- Consumes: the deployed GPU whisper from Task 4.

- [ ] **Step 1: Deploy GPU whisper into the live HU stack**

```bash
ssh 192.168.1.160 'cd ~/StackChan && git pull && \
  podman-compose -f server/podman-compose.hu.yml up -d --force-recreate whisper && \
  sleep 5 && podman ps --filter name=whisper --format "{{.Image}} {{.Status}}"'
```
Expected: image `localhost/stackchan-whisper-hu-cuda-turbo`, status Up.

- [ ] **Step 2: Confirm GPU usage and headroom**

```bash
ssh 192.168.1.160 'curl -sf http://localhost:13000/health; echo; \
  nvidia-smi --query-gpu=memory.used,memory.free --format=csv; \
  nvidia-smi --query-compute-apps=process_name,used_memory --format=csv'
```
Expected: health ok; a second compute app (whisper python) appears; `memory.free` still > 0 with margin; whisper resident < ~1400 MiB.

- [ ] **Step 3: Write the concurrent load script**

Create `server/test_coexistence.sh`:

```bash
#!/usr/bin/env bash
# Concurrent Qwen + Whisper stress: confirms no CUBLAS_ALLOC_FAILED and Qwen perf holds.
set -euo pipefail
QWEN_KEY="${QWEN_KEY:?set QWEN_KEY to the llama-server api key}"

# Background: 20 Hungarian transcriptions in a loop.
( cd "$(dirname "$0")"; for i in $(seq 1 4); do \
    python3 bench_whisper_hu.py --whisper-url http://localhost:13000 \
      --tts-url http://localhost:14000 >/dev/null 2>&1 || echo "WHISPER FAIL iter $i"; \
  done; echo "whisper loop done" ) &
WPID=$!

# Foreground: drive Qwen generation concurrently, time it.
START=$(date +%s.%N)
for i in $(seq 1 4); do
  curl -sf http://localhost:8080/v1/chat/completions \
    -H "Authorization: Bearer ${QWEN_KEY}" -H "Content-Type: application/json" \
    -d '{"messages":[{"role":"user","content":"Sorolj fel öt magyar várost."}],"max_tokens":120}' \
    | python3 -c 'import sys,json; d=json.load(sys.stdin); print("qwen tokens:", d.get("usage",{}).get("completion_tokens"))' \
    || echo "QWEN FAIL iter $i"
done
END=$(date +%s.%N)
wait $WPID
echo "qwen 4x wall time: $(echo "$END - $START" | bc) s"
```

- [ ] **Step 4: Run the coexistence stress and watch for OOM**

```bash
ssh 192.168.1.160 'cd ~/StackChan/server && \
  QWEN_KEY=$(podman inspect qwen-llm --format "{{json .Config.CreateCommand}}" \
    | python3 -c "import sys,json;a=json.load(sys.stdin);print(a[a.index(\"--api-key\")+1])") \
  bash test_coexistence.sh'
ssh 192.168.1.160 'podman logs --tail 40 server_whisper_1 2>&1 | grep -iE "cublas|alloc|error|fail" || echo "no whisper alloc errors"'
ssh 192.168.1.160 'podman logs --tail 40 qwen-llm 2>&1 | grep -iE "cublas|alloc|error|fail" || echo "no qwen alloc errors"'
```
Expected: no `CUBLAS`/alloc/fail lines in either container; whisper loop prints no FAILs; Qwen returns token counts. Note the Qwen wall time and compare to a solo run (re-run the Qwen loop alone if a baseline is needed) — should be within ~10%.

- [ ] **Step 5: Commit the test script**

```bash
git add server/test_coexistence.sh
git commit -m "test(whisper): concurrent Qwen+Whisper GPU coexistence stress"
git push origin main
```

---

### Task 6: End-to-end verification, rollback check, runbook

**Files:**
- Modify: `docs/superpowers/specs/2026-06-19-gpu-hungarian-whisper-design.md` (append a short runbook + measured results)

**Interfaces:**
- Consumes: the deployed, coexistence-proven GPU whisper.

- [ ] **Step 1: End-to-end transcription via the live endpoint**

```bash
ssh 192.168.1.160 'cd ~/StackChan/server && python3 bench_whisper_hu.py \
  --whisper-url http://localhost:13000 --tts-url http://localhost:14000'
```
Expected: low avg WER on Hungarian, latency materially below the CPU baseline (record both numbers).

- [ ] **Step 2: Capture the CPU baseline for the latency claim**

Temporarily run the CPU image on 13002 and bench it, so the "materially better" claim has evidence:

```bash
ssh 192.168.1.160 'podman run -d --rm --name whisper-cpu-base -p 13002:13000 \
  -v whisper-models:/root/.cache/huggingface -v whisper-ct2:/root/.cache/ct2_models \
  localhost/stackchan-whisper-hu-cpu-turbo && sleep 30 && \
  cd ~/StackChan/server && python3 bench_whisper_hu.py --whisper-url http://localhost:13002 \
  --tts-url http://localhost:14000; podman stop whisper-cpu-base'
```
Expected: GPU avg latency << CPU avg latency. Record the ratio.

- [ ] **Step 3: Verify rollback works**

```bash
ssh 192.168.1.160 'cd ~/StackChan && \
  sed -i "s#dockerfile: whisper/Dockerfile.hu.cuda.turbo#dockerfile: whisper/Dockerfile.hu.cpu.turbo#" server/podman-compose.hu.yml && \
  podman-compose -f server/podman-compose.hu.yml up -d --force-recreate whisper && \
  sleep 20 && curl -sf http://localhost:13000/health && echo " CPU rollback OK"; \
  git checkout server/podman-compose.hu.yml && \
  podman-compose -f server/podman-compose.hu.yml up -d --force-recreate whisper'
```
Expected: CPU image serves health OK, then we restore the GPU config and bring it back. Confirms one-line rollback.

- [ ] **Step 4: Record results + runbook in the spec**

Append to `docs/superpowers/specs/2026-06-19-gpu-hungarian-whisper-design.md` a `## Results` section with: chosen model, measured avg WER, GPU vs CPU latency, whisper resident VRAM, Qwen perf under load. Add a `## Runbook` section with the deploy and rollback one-liners actually used.

- [ ] **Step 5: Commit**

```bash
git add docs/superpowers/specs/2026-06-19-gpu-hungarian-whisper-design.md
git commit -m "docs(whisper): record GPU Hungarian ASR results + runbook"
git push origin main
```

---

## Self-Review

**Spec coverage:**
- GPU whisper service (int8_float16, beam 5) → Tasks 1, 4. ✓
- A/B bench (Maxdorger29 vs deepdml) with TTS ground truth + WER → Tasks 2, 3. ✓
- < ~1.4 GB VRAM target → measured in Tasks 3, 5. ✓
- Qwen untouched + coexistence proof → Task 5. ✓
- Latency-beats-CPU success criterion → Task 6 Steps 1-2. ✓
- Compose wiring + rollback → Tasks 4, 6 Step 3. ✓
- Smaller-model contingency → Task 3 Step 8. ✓

**Placeholder scan:** No TBD/TODO; every code step shows full code; commands have expected output. ✓

**Type consistency:** `wer(ref, hyp)` signature used identically in `test_bench_wer.py` and `bench_whisper_hu.py`; `--whisper-url`/`--tts-url` flags consistent across Tasks 2, 3, 5, 6; model ids consistent (`Maxdorger29/whisper-large-v3-turbo-hungarian-lora`, `deepdml/faster-whisper-large-v3-turbo-ct2`). ✓
