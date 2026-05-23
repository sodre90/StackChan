# StackChan Quick Start

Get your StackChan robot talking in under 15 minutes.

---

## What you need

- A StackChan robot (M5Stack CoreS3 + servo body)
- A computer on the same Wi-Fi network as the robot
- [Docker Desktop](https://www.docker.com/products/docker-desktop/) installed
- An LLM — a free [Google Gemini](https://aistudio.google.com/apikey) API key (default),
  **or** a local LLM server such as [Ollama](https://ollama.com)

---

## Step 1 — Choose your LLM

StackChan ships configured for **Google Gemini** (`llm_provider: "gemini"` in
`server/config.yaml`) — the fastest way to get started, with no local model to run.

**Option A — Gemini (default).** Grab a free API key from
[Google AI Studio](https://aistudio.google.com/apikey). You'll paste it into
`additional_config.yaml` in the next step. Nothing to install. If the primary model is
rate-limited, the server automatically falls back through `llm_fallback_models`.

**Option B — Local LLM (Ollama).** Run a model on your own machine instead:

```bash
ollama pull qwen2.5:7b
OLLAMA_HOST=0.0.0.0:8000 ollama serve
```

Then in `server/config.yaml` set `llm_provider: "openai"`, point
`api_base_url` at `http://host.docker.internal:8000/v1`, and set `llm_model` to the
model you pulled. Ollama must listen on port **8000** and be reachable from the containers.

---

## Step 2 — Start the StackChan server stack

```bash
git clone --recurse-submodules https://github.com/sodre90/StackChan
cd StackChan/server
```

If you're using Gemini (Option A), put your API key in `additional_config.yaml` — a
gitignored file that's merged on top of `config.yaml`, so secrets never get committed:

```bash
echo 'api_key: "YOUR_GEMINI_API_KEY"' >> additional_config.yaml
```

Then build and start the containers:

```bash
docker compose up
```

This builds and starts three containers:

| Container | Port | Role |
|-----------|------|------|
| `stackchan` | 12800 | Main server — WebSocket, OTA, AI pipeline |
| `whisper` | 13000 | Speech-to-text (downloads its model on first run) |
| `tts` | 14000 | Text-to-speech |

First startup takes a few minutes while the Whisper model downloads.
Watch progress with `docker compose logs -f whisper`.

Verify everything is up:

```bash
curl http://localhost:12800/xiaozhi/ota/
```

You should get a JSON response with a `websocket.url` field.

---

## Step 3 — Find your computer's IP address

```bash
ipconfig getifaddr en0    # macOS Wi-Fi
# or
hostname -I | awk '{print $1}'  # Linux
```

The ESP32 and your computer must be on the same network. Note this IP — you will need it in the next step.

---

## Step 4 — Configure the firmware

Open `firmware/main/Kconfig.projbuild` and set your server IP:

```
default "http://YOUR_IP:12800/xiaozhi/ota/"
```

Open `firmware/main/hal/utils/secret_logic/secret_logic.cpp` and update:

```cpp
return "http://YOUR_IP:12800";
```

---

## Step 5 — Build and flash the firmware

```bash
cd firmware/

# First time only: fetch submodules and components
git submodule update --init --recursive
python3 fetch_repos.py

# Build (uses Docker internally — no ESP-IDF install needed)
./build.sh build

# Flash — replace with your device's USB port
./build.sh flash /dev/cu.usbmodem1201
```

List USB ports: `ls /dev/cu.usb*` (macOS) or `ls /dev/ttyUSB*` (Linux).

---

## Step 6 — Talk to your robot

1. Power on StackChan
2. Connect it to Wi-Fi via the setup screen
3. It will call the OTA endpoint, receive the WebSocket URL, and connect
4. The display shows the robot face — press the touch panel or speak to start a conversation

---

## Configuration

The AI pipeline is configured in `server/config.yaml`. Open it to change:

- **Language** — `asr_language` (Whisper), `tts_voice` (edge-tts voice), and the response
  language in `system_prompt` (default: English; override in `additional_config.yaml`)
- **LLM** — `llm_provider` (`gemini` or `openai`) and `llm_model`; for Gemini, set
  `llm_fallback_models` for automatic fallback on rate-limits
- **Personality** — `system_prompt`
- **Voice** — `tts_voice` (any [edge-tts voice](https://github.com/rany2/edge-tts))

See [server/CONFIGURATION.md](server/CONFIGURATION.md) for the full reference.

---

## Stopping and restarting

```bash
docker compose down       # stop all containers
docker compose up -d      # start in background
docker compose logs -f    # follow logs
```

The Whisper model is cached in a Docker volume — it will not re-download on restart.

---

## Next steps

- [server/DOCKER_SETUP.md](server/DOCKER_SETUP.md) — Full Docker deployment guide
- [server/CONFIGURATION.md](server/CONFIGURATION.md) — All configuration options
- [server/LOCAL_AI_SETUP.md](server/LOCAL_AI_SETUP.md) — Running services without Docker
