# StackChan Deployment Guide

## Architecture

The **Go server** runs on your computer (or a VPS). The **ESP32 (StackChan)** is the client that connects to it. They are separate devices.

```
┌──────────────────┐              ┌─────────────────┐
│  Your Computer   │              │  ESP32 (StackChan)│
│  Go AI Server    │◄────────────►│  Client Device    │
│  Port 8000       │  WebSocket   │  Microphone+Speaker│
└──────────────────┘              └─────────────────┘
```

---

## Step 1: Build the Go Server

```bash
cd /Users/perdos/prj/StackChan/server
go build -o StackChan main.go
```

---

## Step 2: Find Your Mac's IP Address

```bash
ipconfig getifaddr en0   # WiFi (most common)
# or
ipconfig getifaddr en1   # Ethernet
```

Let's say the IP is `192.168.1.100`.

---

## Step 3: Run the Go Server

```bash
./StackChan
```

The server will:
- Start HTTP on port **8000** (GoFrame default)
- Serve the OTA endpoint at `http://192.168.1.100:8000/xiaozhi/ota/`
- Serve the WebSocket at `ws://192.168.1.100:8000/xiaozhi/ws`
- Print startup diagnostics

---

## Step 4: Configure the ESP32 OTA URL

The ESP32 firmware needs to know where to find your Go server. There are two approaches:

### Option A — Flash with Custom OTA URL (Recommended, One-Time Setup)

```bash
cd /Users/perdos/prj/StackChan/firmware/xiaozhi-esp32
idf.py menuconfig
```

Navigate to: **Component config → OTA URL**

Change the default (`https://api.tenclass.net/xiaozhi/ota/`) to:

```
http://192.168.1.100:8000/xiaozhi/ota/
```

Then flash:

```bash
idf.py -p /dev/cu.usbserial-* flash
```

### Option B — Change OTA URL via NVS (No Reflash Needed)

If the ESP32 is already connected to WiFi, the OTA URL is stored in the NVS "settings" partition. You can change it using the esp-nvs-tool or by modifying the firmware configuration.

---

## Step 5: Connect the ESP32

1. Power on the ESP32
2. It connects to WiFi
3. It calls `GET /xiaozhi/ota/` on your Go server
4. Your server returns the WebSocket URL
5. The ESP32 connects to `ws://192.168.1.100:8000/xiaozhi/ws`
6. Voice interaction begins!

---

## OTA Response Format

The Go server returns this JSON to the ESP32:

```json
{
  "firmware": {
    "version": "1.0.0",
    "url": "http://192.168.1.100:8000/xiaozhi/firmware.bin"
  },
  "websocket": {
    "url": "ws://192.168.1.100:8000/xiaozhi/ws",
    "version": 1
  },
  "server_time": {
    "timestamp": 1745689200,
    "timezone_offset": 0
  }
}
```

---

## MCP Tools (Robot Control)

The following tools are available for the LLM to call:

| Tool | Description | Parameters |
|------|-------------|------------|
| `robot.set_head_angles` | Move robot head | `yaw` (-90 to 90), `pitch` (-45 to 45) |
| `robot.get_head_angles` | Get current head position | None |
| `robot.set_led_color` | Set RGB LED | `red`, `green`, `blue` (0-255) |
| `robot.create_reminder` | Create a timed reminder | `message`, `delay_seconds` |
| `robot.get_reminders` | List active reminders | None |
| `robot.stop_reminder` | Cancel a reminder | `reminder_id` |
| `robot.play_expression` | Play emotion animation | `expression`, `duration` |
| `robot.play_dance` | Play dance sequence | `dance` (default, wave, spin, jump) |

### Expressions
`happy`, `sad`, `angry`, `surprised`, `sleepy`, `thinking`, `love`, `dancing`

### Dances
`default`, `wave`, `spin`, `jump`

---

## VAD (Voice Activity Detection)

### Timeout-based VAD (default)
- Uses configurable silence timeout before processing speech
- Simple and reliable for most use cases
- Configurable via `vad_silence_timeout_ms` (default: 1500ms)

### Energy-based Adaptive VAD
- Analyzes RMS energy of audio frames
- Dynamically adjusts thresholds based on background noise
- Detects speech start, silence, and timeout events
- Available in `vad.go` for future integration

---

## Protocol Overview

### WebSocket Binary Protocol
Format: `[1 byte type][4 bytes length (big-endian)][payload]`

**Message Types:**
- `ControlAvatar (0x03)` — avatar control (LED, expressions)
- `ControlMotion (0x04)` — motion control (head angles)
- `Dance (0x14)` — dance sequences

### XiaoZhi AI Protocol
- Endpoint: `/xiaozhi/ws`
- Text messages: JSON (`hello`, `listen`, `stt`, `tts`, `llm`, `abort`)
- Binary messages: Opus audio chunks

### Message Flow
1. Device → Server: `hello` handshake
2. Server → Device: `hello` response with session ID
3. Device → Server: `listen start` + Opus audio
4. Server → Device: `stt` with transcribed text
5. Server → Device: `tts start` → `tts sentence_start` → Opus audio → `tts stop`

---

## Configuration

Edit `server/manifest/config/config.yaml`:

```yaml
ai:
  api_base_url: "http://localhost:11434/v1"
  api_key: ""
  asr_model: "whisper-large-v3"
  llm_model: "qwen2.5"
  tts_model: "tts-1"
  tts_voice: "alloy"
  tts_response_format: "opus"
  system_prompt: "You are StackChan, a cute AI desktop robot..."
  enable_asr: true
  enable_tts: true
  stream_llm: true
  context_messages: 10
  vad_silence_timeout_ms: 1500
  enable_mcp_tools: true
```

---

## Local Model Setup

### Quick Start with Ollama

```bash
brew install ollama
ollama pull qwen2.5 whisper
ollama serve
cd server && go build && ./StackChan
```

### Supported Backends

| Backend | ASR | LLM | TTS |
|---------|-----|-----|-----|
| Ollama | ✅ whisper | ✅ qwen2.5, llama3, etc. | ❌ |
| LM Studio | ❌ | ✅ Any GGUF model | ❌ |
| vLLM | ❌ | ✅ Any HuggingFace model | ❌ |
| OpenAI | ✅ whisper-1 | ✅ gpt-4o-mini | ✅ tts-1 |
| edge-tts | ❌ | ❌ | ✅ Free TTS |

---

## Troubleshooting

| Problem | Solution |
|---------|----------|
| ESP32 can't reach Go server | Check firewall, use same WiFi network |
| `ota_url` not saved | Flash with `idf.py menuconfig` to set it |
| WebSocket connection fails | Check that `/xiaozhi/ws` handler is registered |
| No audio | Check Opus encoding, sample rate (16kHz), and channel (mono) |
| LLM not responding | Verify Ollama/LM Studio is running and accessible |

---

## Quick Test

Test the OTA endpoint from your Mac:

```bash
curl http://192.168.1.100:8000/xiaozhi/ota/
```

Should return JSON with the websocket URL.

---

## File Structure

| File | Purpose |
|------|---------|
| `server/internal/ai/protocol.go` | WebSocket handler, Opus decode, ASR/LLM/TTS pipeline |
| `server/internal/ai/mcp_tools.go` | MCP tool definitions and handlers |
| `server/internal/ai/vad.go` | Voice activity detection |
| `server/internal/ai/config.go` | Config struct, YAML loading |
| `server/internal/ai/opus_encoder.go` | WAV/PCM utilities |
| `server/internal/cmd/cmd.go` | Server boot, OTA endpoint, handler registration |
| `server/manifest/config/config.yaml` | YAML configuration |
