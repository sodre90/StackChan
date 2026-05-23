# StackChan Server Setup (Podman)

End-to-end guide for running the StackChan AI server stack on a Linux box using `podman-compose`. The stack is fully self-contained — three containers handle the device WebSocket, speech-to-text (Whisper), and text-to-speech (edge-tts). No host-side Go, Python, or model installs are required.

The repo defaults to **English** voice interaction with Google's **Gemini** API as the LLM (with automatic fallback through `llm_fallback_models` on rate-limits). A ready-made **Hungarian** variant ships alongside it — see below. Other settings can also be swapped — see [Customizing the stack](#customizing-the-stack).

### English vs Hungarian

| | Default (English) | Hungarian |
|---|---|---|
| Compose | `podman-compose.yml` | `podman-compose.hu.yml` |
| Whisper model | `large-v3-turbo` (multilingual) | `sarpba/whisper-base-hungarian_v1` |
| Config | `config.yaml` | `config.hu.yaml` |
| TTS voice | `en-US-AvaNeural` | `en-US-AvaMultilingualNeural` |

Both stacks bind the same ports, so run **one at a time**. Secrets live in `additional_config.yaml` and apply to both. The rest of this guide uses the default (English) stack — for Hungarian, just swap `-f podman-compose.yml` → `-f podman-compose.hu.yml` in every command.

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
│  │              │  │ edge-tts     │             │
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

Disk: ~3 GB for images + first-run model downloads (~1 GB for the Whisper model). edge-tts streams from Microsoft's online service, so it needs outbound internet but no local voice model.

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
# LLM provider — "gemini" or "openai"
llm_provider: "gemini"
llm_model:    "gemini-3.5-flash"
# Tried in order when the primary is rate-limited (429) or unavailable (5xx).
# The gemini provider calls Google directly, so api_base_url is not used here.
llm_fallback_models:
  - "gemini-3.1-flash-lite"

# ASR (faster-whisper). Bundled image is Hungarian-tuned; see Customizing to swap.
asr_base_url: "http://whisper:13000/v1"
asr_model:    "whisper"
asr_language: "en"          # "auto" disables the language hint; "hu" for Hungarian

# TTS (edge-tts) — the chosen voice selects the spoken language.
tts_base_url:        "http://tts:14000/v1"
tts_voice:           "en-US-AvaNeural"
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

You can also override **non-secret** keys here to avoid editing the tracked config. (For a complete Hungarian setup, use the Hungarian stack from [English vs Hungarian](#english-vs-hungarian) rather than overriding individual keys.)

Both files are bind-mounted read-only into the `stackchan` container at `/app/config.yaml` and `/app/additional_config.yaml`.

---

## 3. Build and start

```bash
podman-compose -f podman-compose.yml up -d --build
```

> **Important:** always pass an explicit `-f <file>` (`podman-compose.yml` for English, `podman-compose.hu.yml` for Hungarian). With no `-f`, `podman-compose` may pick up `docker-compose.yml` instead.

First build takes ~5–10 minutes (pulls Python base image, installs faster-whisper and edge-tts). Subsequent builds use the layer cache and finish in seconds.

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

# edge-tts — synth a short clip
curl -s -X POST http://localhost:14000/v1/audio/speech \
     -H 'Content-Type: application/json' \
     -d '{"model":"edge","input":"Hello there!","voice":"en-US-AvaNeural","response_format":"opus"}' \
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

For Hungarian, use the Hungarian stack (see [English vs Hungarian](#english-vs-hungarian)) — no edits needed. For a *different* language or model, edit the `CMD` in the relevant Whisper Dockerfile (`whisper/Dockerfile.cpu` for English, `whisper/Dockerfile.cpu.hu` for Hungarian):

```dockerfile
CMD ["python3", "whisper_server.py", \
     "--model", "large-v3-turbo", \
     "--device", "cpu", \
     "--compute-type", "int8", \
     "--port", "13000"]
```

`--model` accepts a faster-whisper shorthand (`tiny`, `base`, `small`, `large-v3`, `large-v3-turbo`) or a HuggingFace repo id. If the model ships a pre-quantized CTranslate2 build in a subfolder, add `--model-subfolder quants/int8_bfloat16` (as the Hungarian model does); otherwise `whisper_server.py` converts it on first start.

Also update `asr_language` in the matching config (`config.yaml` / `config.hu.yaml`). Keep `asr_initial_prompt: ""` unless you're on a large model — small Whisper variants collapse to `.` outputs when given a long priming prompt.

### Different TTS voice / language

edge-tts is voice-driven and needs no rebuild: set `tts_voice` in `config.yaml` (or `additional_config.yaml`) to any [edge-tts voice](https://github.com/rany2/edge-tts) and restart `stackchan`. Examples: `en-US-AvaNeural` (English), `hu-HU-NoemiNeural` (Hungarian), or a multilingual voice like `en-US-AvaMultilingualNeural` that auto-switches by text.

To use local **Piper** TTS instead, point the `tts` service at `piper/Dockerfile` in `podman-compose.yml`, set a Piper voice in `server/piper/Dockerfile`, rebuild the `tts` container, and use that voice file name in `tts_voice`.

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
├── podman-compose.yml      ← English stack (Podman) — use with -f
├── podman-compose.hu.yml   ← Hungarian stack (Podman)
├── docker-compose.yml      ← English stack (Docker Compose)
├── docker-compose.hu.yml   ← Hungarian stack (Docker Compose)
├── config.yaml             ← English config (tracked)
├── config.hu.yaml          ← Hungarian config (tracked)
├── additional_config.yaml  ← secrets (gitignored — create yourself)
├── Dockerfile              ← stackchan (Go) image
├── whisper/
│   ├── Dockerfile.cpu      ← English Whisper image (large-v3-turbo)
│   ├── Dockerfile.cpu.hu   ← Hungarian Whisper image (sarpba base)
│   └── Dockerfile          ← CUDA Whisper image
├── whisper_server.py       ← faster-whisper HTTP wrapper
├── tts/Dockerfile          ← edge-tts image (used by both stacks)
├── tts_server.py           ← edge-tts HTTP wrapper
├── piper/Dockerfile        ← Piper TTS image (optional alternative)
└── piper_server.py         ← Piper HTTP wrapper
```
