# Local AI Setup for StackChan (without Docker)

> **Recommended:** Use Docker Compose instead — see [DOCKER_SETUP.md](DOCKER_SETUP.md).
> This guide is for running services directly on the host, without containers.

This guide explains how to run the full local AI voice pipeline for StackChan:
**microphone → Whisper ASR → LLM (Ollama) → edge-tts → speaker**.

Everything runs on your local machine — no cloud APIs, no API keys required.

---

## Architecture

```
StackChan device  ──WebSocket──►  Go server (:12800)
                                       │
                         ┌─────────────┼─────────────┐
                         ▼             ▼              ▼
                   Whisper ASR    Ollama LLM     edge-tts TTS
                   (:13000)       (:8000)         (:14000)
```

| Service | Port | Purpose |
|---------|------|---------|
| Go server | 12800 | WebSocket handler, OTA endpoint, all coordination |
| Whisper ASR | 13000 | Speech-to-text (faster-whisper) |
| Ollama / LLM | 8000 | Language model (OpenAI-compatible) |
| edge-tts TTS | 14000 | Text-to-speech (Microsoft edge-tts) |

---

## Prerequisites

- **Go 1.24+** — [golang.org/dl](https://golang.org/dl/)
- **Python 3.10+**
- **ffmpeg** — `brew install ffmpeg` (macOS) or `apt install ffmpeg`
- **Ollama** — [ollama.com](https://ollama.com) (or any OpenAI-compatible LLM server)
- A pulled LLM model, e.g. `ollama pull qwen2.5:7b`

---

## Step 1 — Python dependencies

```bash
pip install fastapi uvicorn faster-whisper edge-tts pydub
```

---

## Step 2 — Start the Whisper ASR server

```bash
# From the server/ directory
python3 whisper_server.py --model small --port 13000
```

Available model sizes (smallest → largest, slower → more accurate):
`tiny`, `base`, `small`, `medium`, `large-v3`

For Hungarian or other non-English languages, `small` is a good balance.
Use `medium` for better accuracy at the cost of speed.

Logs are written to stdout. To run in background:
```bash
python3 whisper_server.py --model small --port 13000 > /tmp/whisper.log 2>&1 &
```

---

## Step 3 — Start the TTS server

```bash
python3 tts_server.py --port 14000
```

The default voice is `hu-HU-NoemiNeural` (Hungarian). To use a different language/voice:
```bash
python3 tts_server.py --port 14000 --voice en-US-AvaNeural
```

Browse available voices: `python3 -c "import asyncio, edge_tts; asyncio.run(edge_tts.list_voices())" | grep -i your_language`

---

## Step 4 — Start Ollama (LLM)

```bash
# Ollama serves on port 11434 by default — expose as OpenAI-compatible on 8000
OLLAMA_HOST=0.0.0.0:8000 ollama serve
```

Or if Ollama is already running on 11434, point `api_base_url` in `config.yaml` to `http://localhost:11434/v1`.

Pull a model if you haven't already:
```bash
ollama pull qwen2.5:7b          # fast, good quality
ollama pull llama3.2:3b         # smaller / faster
```

---

## Step 5 — Configure `config.yaml`

Copy and edit the config:

```bash
cp config.yaml config.yaml.local   # optional backup
```

Key settings to change:

```yaml
# Point to your LLM server
api_base_url: "http://localhost:8000/v1"
llm_model: "qwen2.5:7b"           # must match what you pulled in Ollama

# ASR and TTS servers (usually no change needed)
asr_base_url: "http://localhost:13000/v1"
tts_base_url: "http://localhost:14000/v1"

# Language — change to your language
asr_language: "en"                 # e.g. "en", "hu", "de", "fr"
tts_voice: "en-US-AvaNeural"      # matching edge-tts voice name

# System prompt — customize the personality
system_prompt: "You are StackChan, a cute AI desktop robot. Be friendly and concise. Keep responses under 30 words. Never use emojis."

# Enable/disable components
enable_asr: true
enable_tts: true
enable_mcp_tools: true             # weather, datetime tools
```

---

## Step 6 — Build and run the Go server

```bash
cd server/
go build -o StackChan .
./StackChan
```

The server listens on `:12800` by default.

---

## Step 7 — Firmware: build and flash

### 7a — Get dependencies

```bash
cd firmware/

# Pull the xiaozhi-esp32 submodule (our fork with local-server patches)
git submodule update --init --recursive

# Fetch remaining components (mooncake, ArduinoJson, esp-now, etc.)
python3 fetch_repos.py
```

### 7b — Set your server IP

**`firmware/main/Kconfig.projbuild`** — OTA URL:
```
default "http://YOUR_SERVER_IP:12800/xiaozhi/ota/"
```

**`firmware/main/hal/utils/secret_logic/secret_logic.cpp`** — WebSocket server URL:
```cpp
return "http://YOUR_SERVER_IP:12800";
```

The sample rate is already set correctly in `config.h`:
```c
#define AUDIO_INPUT_SAMPLE_RATE  16000
#define AUDIO_OUTPUT_SAMPLE_RATE 16000
```

### 7c — Build (Docker, recommended)

```bash
# First time: builds the Docker image (~5 min)
./build.sh build
```

### 7d — Flash

```bash
./build.sh flash /dev/cu.usbmodem1201   # replace with your device port
```

List available ports:
```bash
ls /dev/cu.usb*
```

---

## MCP Tools (optional)

When `enable_mcp_tools: true`, the LLM can call built-in tools:

| Tool | What it does |
|------|-------------|
| `get_weather` | Current conditions + 3-day forecast via wttr.in |
| `get_current_datetime` | Returns current date/time with weekday |
| `get_price` | Current price + 24h change for crypto (BTC, ETH…) or stocks (AAPL, TSLA…) |
| `web_search` | Searches DuckDuckGo for current news and recent events |

No configuration needed — tools are registered automatically at startup.

---

## Startup script

To start all services at once:

```bash
#!/bin/bash
python3 whisper_server.py --model small --port 13000 > /tmp/whisper.log 2>&1 &
python3 tts_server.py --port 14000 > /tmp/tts.log 2>&1 &
OLLAMA_HOST=0.0.0.0:8000 ollama serve > /tmp/ollama.log 2>&1 &
sleep 3
./StackChan >> /tmp/stackchan.log 2>&1 &
echo "All services started."
```

---

## Troubleshooting

| Symptom | Likely cause | Fix |
|---------|-------------|-----|
| Device connects but no response | LLM not running | Check Ollama is up, model name matches |
| Response in wrong language | `asr_language` or `tts_voice` mismatch | Set both to same language |
| Very long delay before response | VAD not triggering | Speak clearly, pause for ~600ms after finishing |
| Echo loop (device repeating itself) | Echo holdoff too short | Increase `1500*time.Millisecond` in `protocol.go` |
| `listen stop` never received | Normal — device uses server VAD | Working as designed |
