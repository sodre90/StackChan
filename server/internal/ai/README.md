# StackChan AI Protocol Handler

This module implements the [XiaoZhi AI protocol](https://github.com/78/xiaozhi-esp32) for the StackChan Go server, enabling local OpenAI-compatible model support for voice interaction.

## Architecture

```
┌──────────────┐     Opus Audio (binary)     ┌──────────────────────┐
│   ESP32      │ ───────────────────────────▶ │   Go AI Server       │
│ (StackChan)  │ ◀─────────────────────────── │                      │
│  Microphone  │   Opus Audio (TTS response)  │  ASR (Speech→Text)   │
│  Speaker     │                              │  LLM (AI Chat)       │
└──────────────┘                              │  TTS (Text→Speech)   │
                                              │  MCP Tools (Robot)   │
                                              │  VAD (Voice Detect)  │
                                              └──────────┬───────────┘
                                                         │
                                                         │ OpenAI-compatible API
                                                         │
                                              ┌──────────┴───────────┐
                                              │   Local AI Backend   │
                                              │                      │
                                              │  ASR: Whisper        │
                                              │  LLM: Ollama/Qwen    │
                                              │  TTS: OpenAI/edge-tts│
                                              └──────────────────────┘
```

## Features

- **Proper Opus decoding**: Uses `github.com/pion/opus` to decode Opus audio from ESP32 to PCM for ASR
- **Opus TTS output**: Requests Opus format directly from TTS API (no encoding needed)
- **Streaming LLM responses**: Lower latency with streaming enabled by default
- **Conversation context**: Maintains message history per client (configurable)
- **Silence-based VAD**: Configurable silence timeout before processing speech
- **Adaptive VAD**: Energy-based voice activity detection with dynamic thresholds
- **YAML config file**: Full configuration via `manifest/config/config.yaml`
- **MCP Tools**: Robot control tools (head movement, LED, reminders, expressions, dances)
- **Retry logic**: Automatic retry for ASR/LLM/TTS API failures (3 attempts)
- **Error recovery**: Handles device disconnections during speech playback
- **Opus encoder fallback**: Handles PCM/WAV → WAV conversion for TTS

## Protocol Overview

The ESP32 device communicates with the AI backend via WebSocket using the XiaoZhi protocol:

- **Audio**: Opus encoded, 16kHz mono, 60ms frames
- **JSON messages**: `hello`, `listen`, `stt`, `tts`, `llm`, `mcp`, `system`, `abort`
- **Endpoint**: `/xiaozhi/ws` on the Go server

## Setup with Local Models

### 1. Install Local AI Backend

#### Option A: Ollama (LLM + ASR) — Recommended
```bash
# Install Ollama
brew install ollama  # macOS
# or download from https://ollama.ai

# Pull models
ollama pull qwen2.5      # LLM
ollama pull whisper      # ASR (Speech-to-Text)
ollama serve             # Start the server
```

#### Option B: LM Studio (LLM)
Download from https://lmstudio.ai and start the local server.

#### Option C: vLLM (LLM)
```bash
pip install vllm
vllm serve meta-llama/Llama-3.1-8B-Instruct --api-key token-abc123
```

### 2. Configure the Go Server

Edit `server/manifest/config/config.yaml`:

```yaml
ai:
  api_base_url: "http://localhost:11434/v1"  # Ollama API
  api_key: ""                                 # Ollama doesn't require a key
  asr_model: "whisper-large-v3"              # ASR model name
  llm_model: "qwen2.5"                       # LLM model name
  tts_model: "tts-1"                         # TTS model name (if supported)
  tts_voice: "alloy"                         # TTS voice name
  tts_response_format: "opus"                # Opus for ESP32 playback
  system_prompt: "You are StackChan, a cute AI desktop robot..."
  enable_asr: true                           # Enable speech-to-text
  enable_tts: true                           # Enable text-to-speech
  stream_llm: true                           # Stream LLM responses
  context_messages: 10                       # Keep last 10 message pairs
  vad_silence_timeout_ms: 1500               # Silence timeout before processing
  enable_mcp_tools: true                     # Enable robot control tools
```

### 3. Configure the ESP32 Device

The ESP32 device needs to know the AI server URL. This is configured via the OTA endpoint:

1. Set the OTA URL in the ESP32 firmware to point to your Go server
2. The Go server should return the WebSocket URL in the OTA response:

```json
{
  "websocket": {
    "url": "ws://your-server-ip:12800/xiaozhi/ws",
    "version": 1
  }
}
```

### 4. Start the Server

```bash
cd server
go build -o StackChan main.go
./StackChan
```

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

- `happy`, `sad`, `angry`, `surprised`, `sleepy`, `thinking`, `love`, `dancing`

### Dances

- `default`, `wave`, `spin`, `jump`

## VAD (Voice Activity Detection)

The module includes two VAD implementations:

### 1. Timeout-based VAD (default)
- Uses a configurable silence timeout before processing speech
- Simple and reliable for most use cases
- Configurable via `vad_silence_timeout_ms`

### 2. Energy-based Adaptive VAD
- Analyzes RMS energy of audio frames
- Dynamically adjusts thresholds based on background noise
- Detects speech start, silence, and timeout events
- Available via `vad.go` for future integration

## Supported Local AI Backends

| Backend | ASR | LLM | TTS | Setup |
|---------|-----|-----|-----|-------|
| **Ollama** | ✅ whisper | ✅ qwen2.5, llama3, etc. | ❌ | `ollama serve` |
| **LM Studio** | ❌ | ✅ Any GGUF model | ❌ | Start local server |
| **vLLM** | ❌ | ✅ Any HuggingFace model | ❌ | `vllm serve` |
| **Whisper.cpp** | ✅ Local Whisper | ❌ | ❌ | Run whisper-server |
| **edge-tts** | ❌ | ❌ | ✅ Free TTS | `pip install edge-tts` |
| **OpenAI** | ✅ whisper-1 | ✅ gpt-4o-mini | ✅ tts-1 | Cloud API |

## Protocol Message Flow

1. **Device → Server**: `{"type": "hello", "version": 1, "transport": "websocket", "audio_params": {...}}`
2. **Server → Device**: `{"type": "hello", "transport": "websocket", "session_id": "xxx", "audio_params": {...}}`
3. **Device → Server**: `{"type": "listen", "state": "start", "mode": "auto"}` + Opus audio chunks
4. **Server → Device**: `{"type": "stt", "text": "transcribed text"}`
5. **Server → Device**: `{"type": "tts", "state": "start"}`
6. **Server → Device**: `{"type": "tts", "state": "sentence_start", "text": "AI response"}`
7. **Server → Device**: Opus audio chunks (binary)
8. **Server → Device**: `{"type": "tts", "state": "stop"}`

## API Reference

### Config

```go
type Config struct {
    APIBaseURL          string  // OpenAI-compatible API URL
    APIKey              string  // API key (empty for local)
    ASRModel            string  // ASR model (e.g., "whisper-large-v3")
    LLMModel            string  // LLM model (e.g., "qwen2.5")
    TTSModel            string  // TTS model (e.g., "tts-1")
    TTSVoice            string  // TTS voice (e.g., "alloy")
    TTSResponseFormat   string  // TTS output format (mp3, opus, aac, flac, wav, pcm)
    SystemPrompt        string  // LLM system prompt
    EnableASR           bool    // Enable speech-to-text
    EnableTTS           bool    // Enable text-to-speech
    ContextMessages     int     // Number of message pairs to keep in context
    StreamLLM           bool    // Stream LLM responses
    VADSilenceTimeoutMs int     // Silence timeout in ms
    WSPort              int     // WebSocket port (0 = use main server port)
    EnableMCPTools      bool    // Enable MCP robot control tools
}
```

### Functions

```go
// Load config from YAML file (tries default locations if path is empty)
func LoadConfig(path string) (Config, error)

// Initialize the AI protocol handler
func Initialize(config Config)

// Get list of currently connected AI clients
func GetActiveClients() []string

// Create a new VAD detector
func NewVAD(config VADConfig) *VAD

// Create adaptive VAD
func NewAdaptiveVAD() *AdaptiveVAD
```

## Files

| File | Purpose |
|------|---------|
| `config.go` | Configuration struct, default config, YAML loading |
| `protocol.go` | WebSocket handler, Opus decode, ASR/LLM/TTS pipeline, error recovery |
| `opus_encoder.go` | PCM ↔ Opus conversion utilities, WAV parsing |
| `vad.go` | Voice activity detection (energy-based + adaptive) |
| `mcp_tools.go` | MCP tool definitions and handlers for robot control |
| `README.md` | This documentation |

## Notes

- The Opus decoder is initialized per-client connection
- TTS requests Opus format directly from the API (no encoding needed)
- Conversation context is maintained per client and trimmed to configured size
- MCP tools send binary control messages to the ESP32 device
- Error recovery handles device disconnections during audio playback
- For production use, consider adding proper VAD (voice activity detection)
- The ESP32 sends Opus-encoded audio which is decoded to PCM for ASR
- TTS responses are requested in Opus format for direct playback on ESP32
