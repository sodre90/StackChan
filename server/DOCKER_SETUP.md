# Docker Deployment Guide

Run the entire StackChan server stack in containers — no Python, Go, or system library installs needed on the host.

---

## Architecture

```
Host machine
├── Ollama (LLM)           port 8000   ← runs on host, NOT in Docker
└── Docker Compose
    ├── stackchan          port 12800  ← Go server, WebSocket, OTA
    ├── whisper            port 13000  ← Whisper ASR (speech-to-text)
    └── tts                port 14000  ← edge-tts (text-to-speech)
```

The LLM server (Ollama) stays on the host because it needs GPU/ANE access.
Containers reach it via `host.docker.internal:8000`.

---

## Prerequisites

- [Docker Desktop](https://www.docker.com/products/docker-desktop/) (Mac/Windows) or Docker Engine + Compose plugin (Linux)
- An LLM server running on the host at port 8000 (see [LOCAL_AI_SETUP.md](LOCAL_AI_SETUP.md) for Ollama setup)

---

## Quick start

```bash
cd server/
docker compose up
```

On first run this will:
1. Build all three images (~3–5 minutes)
2. Download the Whisper large-v3 model from HuggingFace (~3 GB) — only once
3. Start all containers

To run in the background:

```bash
docker compose up -d
```

---

## Verifying services

```bash
# Go server — OTA endpoint
curl http://localhost:12800/xiaozhi/ota/

# Whisper — transcription (needs a wav file)
curl -X POST http://localhost:13000/v1/audio/transcriptions \
  -F "file=@sample.wav" -F "model=whisper" -F "language=en"

# TTS — synthesise speech
curl -X POST http://localhost:14000/v1/audio/speech \
  -H "Content-Type: application/json" \
  -d '{"model":"edge-tts","input":"Hello!","voice":"en-US-AvaNeural","response_format":"opus"}' \
  -o /tmp/test.ogg && ffplay -nodisp -autoexit /tmp/test.ogg
```

---

## Container details

### stackchan (port 12800)

Built from `Dockerfile` — multi-stage Go build.

- Serves the OTA endpoint at `GET /xiaozhi/ota/`
- Handles the AI WebSocket at `ws://HOST:12800/xiaozhi/ws`
- Handles the app/device relay WebSocket at `ws://HOST:12800/stackChan/ws`
- Reads `config.yaml` mounted from `./config.yaml`
- Reaches the LLM via `host.docker.internal:8000`

### whisper (port 13000)

Built from `whisper/Dockerfile` — Python + faster-whisper.

- `POST /v1/audio/transcriptions` — transcribe a WAV file
- `GET /health` — liveness check
- Model files cached in the `whisper-models` Docker volume (persists across restarts)
- Defaults to `large-v3`; change in `whisper/Dockerfile` CMD if you want a smaller model

### tts (port 14000)

Built from `tts/Dockerfile` — Python + edge-tts + ffmpeg.

- `POST /v1/audio/speech` — synthesise text to Opus audio
- Accepts any [edge-tts voice name](https://github.com/rany2/edge-tts) directly, plus OpenAI-compatible aliases (`alloy`, `echo`, etc.)
- Converts output to 16 kHz mono Opus via ffmpeg (compatible with ESP32)

---

## Configuration

Edit `server/config.yaml` and restart:

```bash
docker compose restart stackchan
```

The config file is bind-mounted into the container — no rebuild needed for config changes.
See [CONFIGURATION.md](CONFIGURATION.md) for all options.

---

## Rebuilding after code changes

```bash
# Rebuild a single service
docker compose build stackchan

# Rebuild all and restart
docker compose up --build

# Force a full rebuild (no cache)
docker compose build --no-cache
```

---

## Updating the Whisper model size

Edit `whisper/Dockerfile`:

```dockerfile
CMD ["python3", "whisper_server.py", "--model", "small", "--port", "13000"]
```

Available sizes: `tiny`, `base`, `small`, `medium`, `large-v3`

Then rebuild:

```bash
docker compose build whisper
docker compose up -d whisper
```

The new model downloads on first startup and is cached in the `whisper-models` volume.
To download a fresh model, remove the old volume first:

```bash
docker compose down
docker volume rm server_whisper-models
docker compose up
```

---

## Logs

```bash
docker compose logs -f              # all services
docker compose logs -f stackchan    # Go server only
docker compose logs -f whisper      # ASR only
docker compose logs -f tts          # TTS only
```

---

## Stopping

```bash
docker compose down          # stop containers, keep volumes
docker compose down -v       # stop containers and delete volumes (re-downloads Whisper model)
```

---

## Troubleshooting

| Symptom | Cause | Fix |
|---------|-------|-----|
| `address already in use :12800` | Old server process running | `kill $(lsof -ti :12800)` |
| Whisper keeps restarting | Port conflict or model not downloaded | Check `docker compose logs whisper` |
| `host.docker.internal: no route` | LLM not reachable from container | Ensure Ollama binds `0.0.0.0`, not `127.0.0.1` |
| TTS returns empty audio | Voice name typo | Check `tts_voice` in config.yaml against edge-tts voice list |
| Whisper returns empty text for clear speech | Wrong language hint | Set `asr_language` to the correct ISO 639-1 code |
| Container exits with `opusfile` errors | Old stackchan image | `docker compose build --no-cache stackchan` |
