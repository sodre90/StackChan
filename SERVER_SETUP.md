# StackChan Server Setup (Podman)

End-to-end guide for running the StackChan AI server stack on a Linux box using `podman-compose`. The stack is fully self-contained — three containers handle the device WebSocket, speech-to-text (Whisper), and text-to-speech (Piper). No host-side Go, Python, or model installs are required.

The defaults are tuned for **Hungarian** voice interaction with Google's **Gemini** API as the LLM. Both can be swapped — see [Customizing the stack](#customizing-the-stack).

---

## What you'll end up with

```
┌─────────────────────────────────────────────────┐
│  Server (any Linux box reachable on your LAN)   │
│  ┌──────────────┐  ┌──────────────┐             │
│  │ stackchan    │  │ whisper      │             │
│  │ :12800       │──│ :13000       │             │
│  │ (Go,         │  │ faster-      │             │
│  │  WebSocket,  │  │ whisper CPU  │             │
│  │  OTA)        │  │ int8_bfloat16│             │
│  │              │  │              │             │
│  │              │  ┌──────────────┐             │
│  │              │──│ tts :14000   │             │
│  │              │  │ Piper        │             │
│  └──────┬───────┘  └──────────────┘             │
└─────────┼───────────────────────────────────────┘
          │ WebSocket /xiaozhi/ws
          ▼
   M5Stack CoreS3 (StackChan)
```

---

## Prerequisites

On the server:

| Tool                | Tested version | Install                                                  |
| ------------------- | -------------- | -------------------------------------------------------- |
| `podman`            | 4.x or 5.x     | `dnf install podman` / `apt install podman`              |
| `podman-compose`    | 1.0+           | `pip install podman-compose` or distro package           |
| `git`               | any            | `dnf install git` / `apt install git`                    |

Disk: ~3 GB for images + first-run model downloads (~1 GB for Whisper, ~100 MB for the Piper voice).

Ports `12800`, `13000`, `14000` must be open inbound on the LAN.

> The CPU Whisper image (`whisper/Dockerfile.cpu`) is the supported path here. There's also a CUDA variant in `docker-compose.yml` if you have an NVIDIA GPU on the host — see the original `server/DOCKER_SETUP.md`.

---

## 1. Clone the repo

```bash
git clone https://github.com/<your-fork>/StackChan.git
cd StackChan/server
```

All commands below are run from `StackChan/server/`.

---

## 2. Configure

### 2a. `config.yaml` — non-secret settings (tracked in git)

The repo ships with a working `config.yaml`. The fields most people want to tweak:

```yaml
# LLM provider — "gemini" or "openai-compatible"
llm_provider: "gemini"
api_base_url: "https://generativelanguage.googleapis.com/v1beta"
llm_model:    "gemini-3.1-flash-lite"

# ASR (faster-whisper, Hungarian-tuned base model by default)
asr_base_url: "http://whisper:13000/v1"
asr_model:    "whisper"
asr_language: "hu"          # "auto" disables the language hint

# TTS (Piper)
tts_base_url:        "http://tts:14000/v1"
tts_voice:           "hu_HU-anna-medium"
tts_response_format: "opus"

# System prompt fed to the LLM
system_prompt: >
  You are StackChan, a cute AI desktop robot...

# Optional Home Assistant integration
ha_url: "http://192.168.1.165:8123"
```

### 2b. `additional_config.yaml` — secrets (gitignored)

Create this file alongside `config.yaml`. It is loaded **after** `config.yaml` and overlays/overrides any matching keys, so put API keys and other secrets here.

```yaml
api_key: "AIza...your-gemini-key..."
brave_search_api_key: "BSA...optional, enables real web search..."
ha_token: "eyJ...optional, long-lived Home Assistant token..."
```

Both files are bind-mounted read-only into the `stackchan` container at `/app/config.yaml` and `/app/additional_config.yaml`.

---

## 3. Build and start

```bash
podman-compose -f podman-compose.yml up -d --build
```

> **Important:** always pass `-f podman-compose.yml`. If you omit it, `podman-compose` picks up `docker-compose.yml` instead, which targets the CUDA build of Whisper and will fail on a CPU-only host.

First build takes ~5–10 minutes (pulls Python base image, installs faster-whisper, downloads the Piper voice). Subsequent builds use the layer cache and finish in seconds.

On first start, the Whisper container downloads the pre-quantized model (`sarpba/whisper-base-hungarian_v1`, `quants/int8_bfloat16` subfolder) into the `whisper-models` named volume — that takes another ~30 s.

Check it's running:

```bash
podman-compose -f podman-compose.yml ps
podman logs server_stackchan_1   | tail
podman logs server_whisper_1     | tail
podman logs server_tts_1         | tail
```

You should see something like:

```
INFO:whisper-server:Using pre-quantized model at /root/.cache/ct2_models/.../quants/int8_bfloat16
INFO:whisper-server:Model loaded.
INFO:     Uvicorn running on http://0.0.0.0:13000
```

### Smoke-test the services

```bash
# Go server — OTA endpoint should respond with JSON
curl http://localhost:12800/xiaozhi/ota/ -H 'Device-Id: aa:bb:cc:dd:ee:ff'

# Whisper — transcribe a sample (an mp3 ships in server/whisper/test-voice.mp3)
curl -s -X POST http://localhost:13000/v1/audio/transcriptions \
     -F 'file=@whisper/test-voice.mp3' -F 'language=hu' -F 'model=whisper'
# → {"text":"Szia, Laci, várod már a nyaralást?", ...}

# Piper TTS — synth a short clip
curl -s -X POST http://localhost:14000/v1/audio/speech \
     -H 'Content-Type: application/json' \
     -d '{"model":"piper","input":"Sziasztok!","voice":"hu_HU-anna-medium","response_format":"opus"}' \
     -o /tmp/test.ogg
```

---

## 4. Point your StackChan device at the server

The factory-flashed firmware looks up its WebSocket endpoint over OTA. Two ways to redirect it:

1. **Reflash with a custom OTA URL** — recommended. Build the firmware in `firmware/xiaozhi-esp32`, change `CONFIG_OTA_URL` to `http://<SERVER_IP>:12800/xiaozhi/ota/` in `menuconfig`, and flash. See `DEPLOYMENT.md` at repo root for the full firmware build steps.
2. **Edit NVS on a flashed device** — set the `ota_url` key in the device's `settings` NVS namespace. Useful if you don't want to reflash, but requires an NVS dump tool.

If you previously connected the device to a different server, fully erase NVS first (`esptool erase_region 0x9000 0x4000`) to wipe the cached WebSocket URL.

---

## 5. Updating / rebuilding

Pull and rebuild whenever you change config or code:

```bash
git pull
podman rm -f server_stackchan_1 server_whisper_1 server_tts_1   # see note below
podman-compose -f podman-compose.yml up -d --build
```

> **Why force-remove first?** `restart: unless-stopped` keeps old containers alive; `podman-compose up -d` will then reuse them with the *old* image even after a successful rebuild. Always `podman rm -f` the containers whose code or `CMD` changed.

If only `config.yaml` or `additional_config.yaml` changed, you don't need to rebuild — just restart `stackchan`:

```bash
podman restart server_stackchan_1
```

---

## Customizing the stack

### Different LLM

Switch `llm_provider`, `api_base_url`, and `llm_model` in `config.yaml`. For a local model:

```yaml
llm_provider:  "openai-compatible"
api_base_url:  "http://host.containers.internal:8000/v1"   # llama-server on the host
llm_model:     "your-local-model-alias"
```

`host.containers.internal` resolves to the host inside rootful and rootless Podman 4.x+.

### Different language / Whisper model

Edit `server/whisper/Dockerfile.cpu` and change the `CMD` line:

```dockerfile
CMD ["python3", "whisper_server.py", \
     "--model", "openai/whisper-large-v3-turbo", \
     "--device", "cpu", \
     "--compute-type", "int8_float32", \
     "--port", "13000"]
```

If the model ships a pre-quantized CTranslate2 build in a HuggingFace subfolder, point at it with `--model-subfolder quants/int8_bfloat16` (mirrors what the default `sarpba/whisper-base-hungarian_v1` setup does). Otherwise `whisper_server.py` will auto-convert the PyTorch model on first start.

Also update `asr_language` in `config.yaml`. Set `asr_initial_prompt: ""` unless you're on a large model — small Whisper variants collapse to `.` outputs when given a long priming prompt.

### Different TTS voice

Piper voices live at <https://huggingface.co/rhasspy/piper-voices>. Update the two `curl` lines in `server/piper/Dockerfile` to download a different `.onnx` and `.onnx.json`, change the `--voice` flag in the `CMD`, and rebuild the `tts` container. Then point `tts_voice` in `config.yaml` at the new voice file name.

---

## Troubleshooting

**`Error: creating build container: short-name resolution enforced but cannot prompt without a TTY`**
You ran `podman-compose up` without `-f podman-compose.yml` and it picked up `docker-compose.yml`. Use the explicit flag — see step 3.

**Whisper transcription returns only `"."`**
The current Whisper model is sensitive to long `asr_initial_prompt` values. Empty it in `config.yaml`. (This is the same fix that gets applied when switching from a `large-v3-turbo` model to a `base` model.)

**LED stays blue on the device long after the response finishes**
Means `tts.stop` arrived while the device's audio buffer still had frames queued. Check the audio playout deadline math in `server/internal/ai/protocol.go` — `audioPlayoutDeadline` should be advanced by `sentFrames * OpusFrameDuration` in `sendAudioChunks`, and `processLLMResponse` should sleep until that time before sending `tts.stop`.

**Audio stutters mid-playback**
The server bursts the first `opusBurstFrames` frames at sentence start to build a device-side jitter buffer, then sends the rest at `opusFrameDelayMs`. If you change one, change the other accordingly: too-short burst → underruns; too-fast steady rate → TCP backpressure and crackling.

**`podman-compose up` succeeds but the new image isn't being used**
A previous container from the old image is still running. `podman rm -f <container>` and re-run.

---

## File map

```
server/
├── podman-compose.yml      ← compose file (CPU build) — use with -f
├── docker-compose.yml      ← CUDA build (NVIDIA GPU on host)
├── config.yaml             ← main config (tracked)
├── additional_config.yaml  ← secrets (gitignored — create yourself)
├── Dockerfile              ← stackchan (Go) image
├── whisper/
│   ├── Dockerfile.cpu      ← CPU Whisper image (used by podman-compose)
│   └── Dockerfile          ← CUDA Whisper image
├── whisper_server.py       ← faster-whisper HTTP wrapper
├── piper/Dockerfile        ← Piper TTS image
└── piper_server.py         ← Piper HTTP wrapper
```
