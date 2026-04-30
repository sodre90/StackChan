# StackChan Quick Start

Get your StackChan robot talking in under 15 minutes.

---

## What you need

- A StackChan robot (M5Stack CoreS3 + servo body)
- A computer on the same Wi-Fi network as the robot
- [Docker Desktop](https://www.docker.com/products/docker-desktop/) installed
- An LLM server — [Ollama](https://ollama.com) is the easiest option

---

## Step 1 — Start a local LLM

```bash
# Install Ollama from https://ollama.com, then:
ollama pull qwen2.5:7b
OLLAMA_HOST=0.0.0.0:8000 ollama serve
```

Ollama must listen on port **8000** and be reachable from Docker containers.
If you use a different LLM server, update `api_base_url` in `server/config.yaml`.

---

## Step 2 — Start the StackChan server stack

```bash
git clone --recurse-submodules https://github.com/sodre90/StackChan
cd StackChan/server
docker compose up
```

This builds and starts three containers:

| Container | Port | Role |
|-----------|------|------|
| `stackchan` | 12800 | Main server — WebSocket, OTA, AI pipeline |
| `whisper` | 13000 | Speech-to-text (downloads ~3 GB model on first run) |
| `tts` | 14000 | Text-to-speech (edge-tts, Microsoft neural voices) |

First startup takes a few minutes while the Whisper large-v3 model downloads.
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

- **Language** — `asr_language` and `tts_voice` (default: Hungarian)
- **LLM model** — `llm_model` (must match what you pulled in Ollama)
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
